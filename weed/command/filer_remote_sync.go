package command

import (
	"context"
	"fmt"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	"github.com/chrislusf/seaweedfs/weed/pb/remote_pb"
	"github.com/chrislusf/seaweedfs/weed/replication/source"
	"github.com/chrislusf/seaweedfs/weed/security"
	"github.com/chrislusf/seaweedfs/weed/util"
	"google.golang.org/grpc"
	"os"
	"time"
)

type RemoteSyncOptions struct {
	filerAddress             *string
	grpcDialOption           grpc.DialOption
	readChunkFromFiler       *bool
	debug                    *bool
	timeAgo                  *time.Duration
	dir                      *string
	createBucketAt           *string
	createBucketRandomSuffix *bool

	mappings    *remote_pb.RemoteStorageMapping
	remoteConfs map[string]*remote_pb.RemoteConf
	bucketsDir  string
}

var _ = filer_pb.FilerClient(&RemoteSyncOptions{})

func (option *RemoteSyncOptions) WithFilerClient(fn func(filer_pb.SeaweedFilerClient) error) error {
	return pb.WithFilerClient(pb.ServerAddress(*option.filerAddress), option.grpcDialOption, func(client filer_pb.SeaweedFilerClient) error {
		return fn(client)
	})
}
func (option *RemoteSyncOptions) AdjustedUrl(location *filer_pb.Location) string {
	return location.Url
}

var (
	remoteSyncOptions RemoteSyncOptions
)

func init() {
	cmdFilerRemoteSynchronize.Run = runFilerRemoteSynchronize // break init cycle
	remoteSyncOptions.filerAddress = cmdFilerRemoteSynchronize.Flag.String("filer", "localhost:8888", "filer of the SeaweedFS cluster")
	remoteSyncOptions.dir = cmdFilerRemoteSynchronize.Flag.String("dir", "", "a mounted directory on filer")
	remoteSyncOptions.createBucketAt = cmdFilerRemoteSynchronize.Flag.String("createBucketAt", "", "one remote storage name to create new buckets in")
	remoteSyncOptions.createBucketRandomSuffix = cmdFilerRemoteSynchronize.Flag.Bool("createBucketWithRandomSuffix", true, "add randomized suffix to bucket name to avoid conflicts")
	remoteSyncOptions.readChunkFromFiler = cmdFilerRemoteSynchronize.Flag.Bool("filerProxy", false, "read file chunks from filer instead of volume servers")
	remoteSyncOptions.debug = cmdFilerRemoteSynchronize.Flag.Bool("debug", false, "debug mode to print out filer updated remote files")
	remoteSyncOptions.timeAgo = cmdFilerRemoteSynchronize.Flag.Duration("timeAgo", 0, "start time before now. \"300ms\", \"1.5h\" or \"2h45m\". Valid time units are \"ns\", \"us\" (or \"µs\"), \"ms\", \"s\", \"m\", \"h\"")
}

var cmdFilerRemoteSynchronize = &Command{
	UsageLine: "filer.remote.sync",
	Short:     "resumable continuously write back updates to remote storage",
	Long: `resumable continuously write back updates to remote storage

	filer.remote.sync listens on filer update events. 
	If any mounted remote file is updated, it will fetch the updated content,
	and write to the remote storage.

	There are two modes:

	1)By default, watch /buckets folder and write back all changes.

		# if there is only one remote storage configured
		weed filer.remote.sync
		# if there are multiple remote storages configured
		# specify a remote storage to create new buckets.
		weed filer.remote.sync -createBucketAt=cloud1

	2)Write back one mounted folder to remote storage

		weed filer.remote.sync -dir=/mount/s3_on_cloud


`,
}

func runFilerRemoteSynchronize(cmd *Command, args []string) bool {

	util.LoadConfiguration("security", false)
	grpcDialOption := security.LoadClientTLS(util.GetViper(), "grpc.client")
	remoteSyncOptions.grpcDialOption = grpcDialOption

	dir := *remoteSyncOptions.dir
	filerAddress := pb.ServerAddress(*remoteSyncOptions.filerAddress)

	filerSource := &source.FilerSource{}
	filerSource.DoInitialize(
		filerAddress.ToHttpAddress(),
		filerAddress.ToGrpcAddress(),
		"/", // does not matter
		*remoteSyncOptions.readChunkFromFiler,
	)

	remoteSyncOptions.bucketsDir = "/buckets"
	// check buckets again
	remoteSyncOptions.WithFilerClient(func(filerClient filer_pb.SeaweedFilerClient) error {
		resp, err := filerClient.GetFilerConfiguration(context.Background(), &filer_pb.GetFilerConfigurationRequest{})
		if err != nil {
			return err
		}
		remoteSyncOptions.bucketsDir = resp.DirBuckets
		return nil
	})

	if dir != "" && dir != remoteSyncOptions.bucketsDir {
		fmt.Printf("synchronize %s to remote storage...\n", dir)
		util.RetryForever("filer.remote.sync "+dir, func() error {
			return followUpdatesAndUploadToRemote(&remoteSyncOptions, filerSource, dir)
		}, func(err error) bool {
			if err != nil {
				glog.Errorf("synchronize %s: %v", dir, err)
			}
			return true
		})
		return true
	}

	// read filer remote storage mount mappings
	if detectErr := remoteSyncOptions.collectRemoteStorageConf(); detectErr != nil {
		fmt.Fprintf(os.Stderr, "read mount info: %v\n", detectErr)
		return true
	}

	// synchronize /buckets folder
	fmt.Printf("synchronize buckets in %s ...\n", remoteSyncOptions.bucketsDir)
	util.RetryForever("filer.remote.sync buckets", func() error {
		return remoteSyncOptions.followBucketUpdatesAndUploadToRemote(filerSource)
	}, func(err error) bool {
		if err != nil {
			glog.Errorf("synchronize %s: %v", remoteSyncOptions.bucketsDir, err)
		}
		return true
	})
	return true

}
