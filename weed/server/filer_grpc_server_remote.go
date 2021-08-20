package weed_server

import (
	"context"
	"fmt"
	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/operation"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	"github.com/chrislusf/seaweedfs/weed/pb/volume_server_pb"
	"github.com/chrislusf/seaweedfs/weed/storage/needle"
	"github.com/chrislusf/seaweedfs/weed/util"
	"github.com/golang/protobuf/proto"
	"strings"
	"time"
)

func (fs *FilerServer) DownloadToLocal(ctx context.Context, req *filer_pb.DownloadToLocalRequest) (*filer_pb.DownloadToLocalResponse, error) {

	// load all mappings
	mappingEntry, err := fs.filer.FindEntry(ctx, util.JoinPath(filer.DirectoryEtcRemote, filer.REMOTE_STORAGE_MOUNT_FILE))
	if err != nil {
		return nil, err
	}
	mappings, err := filer.UnmarshalRemoteStorageMappings(mappingEntry.Content)
	if err != nil {
		return nil, err
	}

	// find mapping
	var remoteStorageMountedLocation *filer_pb.RemoteStorageLocation
	var localMountedDir string
	for k, loc := range mappings.Mappings {
		if strings.HasPrefix(req.Directory, k) {
			localMountedDir, remoteStorageMountedLocation = k, loc
		}
	}
	if localMountedDir == "" {
		return nil, fmt.Errorf("%s is not mounted", req.Directory)
	}

	// find storage configuration
	storageConfEntry, err := fs.filer.FindEntry(ctx, util.JoinPath(filer.DirectoryEtcRemote, remoteStorageMountedLocation.Name+filer.REMOTE_STORAGE_CONF_SUFFIX))
	if err != nil {
		return nil, err
	}
	storageConf := &filer_pb.RemoteConf{}
	if unMarshalErr := proto.Unmarshal(storageConfEntry.Content, storageConf); unMarshalErr != nil {
		return nil, fmt.Errorf("unmarshal remote storage conf %s/%s: %v", filer.DirectoryEtcRemote, remoteStorageMountedLocation.Name+filer.REMOTE_STORAGE_CONF_SUFFIX, unMarshalErr)
	}

	// find the entry
	entry, err := fs.filer.FindEntry(ctx, util.JoinPath(req.Directory, req.Name))
	if err == filer_pb.ErrNotFound {
		return nil, err
	}

	resp := &filer_pb.DownloadToLocalResponse{}
	if entry.Remote == nil || entry.Remote.RemoteSize == 0 {
		return resp, nil
	}

	// detect storage option
	// replication level is set to "000" to ensure only need to ask one volume server to fetch the data.
	so, err := fs.detectStorageOption(req.Directory, "", "000", 0, "", "", "")
	if err != nil {
		return resp, err
	}
	assignRequest, altRequest := so.ToAssignRequests(1)

	// find a good chunk size
	chunkSize := int64(5 * 1024 * 1024)
	chunkCount := entry.Remote.RemoteSize/chunkSize + 1
	for chunkCount > 1000 && chunkSize < int64(fs.option.MaxMB)*1024*1024/2 {
		chunkSize *= 2
		chunkCount = entry.Remote.RemoteSize/chunkSize + 1
	}

	dest := util.FullPath(remoteStorageMountedLocation.Path).Child(string(util.FullPath(req.Directory).Child(req.Name))[len(localMountedDir):])

	var chunks []*filer_pb.FileChunk
	var fetchAndWriteErr error

	limitedConcurrentExecutor := util.NewLimitedConcurrentExecutor(8)
	for offset := int64(0); offset < entry.Remote.RemoteSize; offset += chunkSize {
		localOffset := offset

		limitedConcurrentExecutor.Execute(func() {
			size := chunkSize
			if localOffset+chunkSize > entry.Remote.RemoteSize {
				size = entry.Remote.RemoteSize - localOffset
			}

			// assign one volume server
			assignResult, err := operation.Assign(fs.filer.GetMaster, fs.grpcDialOption, assignRequest, altRequest)
			if err != nil {
				fetchAndWriteErr = err
				return
			}
			if assignResult.Error != "" {
				fetchAndWriteErr = fmt.Errorf("assign: %v", assignResult.Error)
				return
			}
			fileId, parseErr := needle.ParseFileIdFromString(assignResult.Fid)
			if assignResult.Error != "" {
				fetchAndWriteErr = fmt.Errorf("unrecognized file id %s: %v", assignResult.Fid, parseErr)
				return
			}

			// tell filer to tell volume server to download into needles
			err = operation.WithVolumeServerClient(assignResult.Url, fs.grpcDialOption, func(volumeServerClient volume_server_pb.VolumeServerClient) error {
				_, fetchAndWriteErr := volumeServerClient.FetchAndWriteNeedle(context.Background(), &volume_server_pb.FetchAndWriteNeedleRequest{
					VolumeId:     uint32(fileId.VolumeId),
					NeedleId:     uint64(fileId.Key),
					Cookie:       uint32(fileId.Cookie),
					Offset:       localOffset,
					Size:         size,
					RemoteType:   storageConf.Type,
					RemoteName:   storageConf.Name,
					S3AccessKey:  storageConf.S3AccessKey,
					S3SecretKey:  storageConf.S3SecretKey,
					S3Region:     storageConf.S3Region,
					S3Endpoint:   storageConf.S3Endpoint,
					RemoteBucket: remoteStorageMountedLocation.Bucket,
					RemotePath:   string(dest),
				})
				if fetchAndWriteErr != nil {
					return fmt.Errorf("volume server %s fetchAndWrite %s: %v", assignResult.Url, dest, fetchAndWriteErr)
				}
				return nil
			})

			if err != nil {
				fetchAndWriteErr = err
				return
			}

			chunks = append(chunks, &filer_pb.FileChunk{
				FileId: assignResult.Fid,
				Offset: localOffset,
				Size:   uint64(size),
				Mtime:  time.Now().Unix(),
				Fid: &filer_pb.FileId{
					VolumeId: uint32(fileId.VolumeId),
					FileKey:  uint64(fileId.Key),
					Cookie:   uint32(fileId.Cookie),
				},
			})
		})
	}

	if fetchAndWriteErr != nil {
		return nil, fetchAndWriteErr
	}

	garbage := entry.Chunks

	newEntry := entry.ShallowClone()
	newEntry.Chunks = chunks
	newEntry.Remote = proto.Clone(entry.Remote).(*filer_pb.RemoteEntry)
	newEntry.Remote.LastLocalSyncTsNs = time.Now().UnixNano()

	// this skips meta data log events

	if err := fs.filer.Store.UpdateEntry(context.Background(), newEntry); err != nil {
		return nil, err
	}
	fs.filer.DeleteChunks(garbage)

	fs.filer.NotifyUpdateEvent(ctx, entry, newEntry, true, false, nil)

	resp.Entry = newEntry.ToProtoEntry()

	return resp, nil

}
