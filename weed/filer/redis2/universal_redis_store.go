package redis2

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	"github.com/chrislusf/seaweedfs/weed/util"
)

const (
	DIR_LIST_MARKER = "\x00"
)

type UniversalRedis2Store struct {
	Client                  redis.UniversalClient
	superLargeDirectoryHash map[string]bool
}

func (store *UniversalRedis2Store) isSuperLargeDirectory(dir string) (isSuperLargeDirectory bool) {
	_, isSuperLargeDirectory = store.superLargeDirectoryHash[dir]
	return
}

func (store *UniversalRedis2Store) loadSuperLargeDirectories(superLargeDirectories []string) {
	// set directory hash
	store.superLargeDirectoryHash = make(map[string]bool)
	for _, dir := range superLargeDirectories {
		store.superLargeDirectoryHash[dir] = true
	}
}

func (store *UniversalRedis2Store) BeginTransaction(ctx context.Context) (context.Context, error) {
	return ctx, nil
}
func (store *UniversalRedis2Store) CommitTransaction(ctx context.Context) error {
	return nil
}
func (store *UniversalRedis2Store) RollbackTransaction(ctx context.Context) error {
	return nil
}

func (store *UniversalRedis2Store) InsertEntry(ctx context.Context, entry *filer.Entry) (err error) {

	value, err := entry.EncodeAttributesAndChunks()
	if err != nil {
		return fmt.Errorf("encoding %s %+v: %v", entry.FullPath, entry.Attr, err)
	}

	if len(entry.Chunks) > 50 {
		value = util.MaybeGzipData(value)
	}

	if err = store.Client.Set(ctx, string(entry.FullPath), value, time.Duration(entry.TtlSec)*time.Second).Err(); err != nil {
		return fmt.Errorf("persisting %s : %v", entry.FullPath, err)
	}

	dir, name := entry.FullPath.DirAndName()
	if store.isSuperLargeDirectory(dir) {
		return nil
	}

	if name != "" {
		if err = store.Client.ZAddNX(ctx, genDirectoryListKey(dir), &redis.Z{Score: 0, Member: name}).Err(); err != nil {
			return fmt.Errorf("persisting %s in parent dir: %v", entry.FullPath, err)
		}
	}

	return nil
}

func (store *UniversalRedis2Store) UpdateEntry(ctx context.Context, entry *filer.Entry) (err error) {

	return store.InsertEntry(ctx, entry)
}

func (store *UniversalRedis2Store) FindEntry(ctx context.Context, fullpath util.FullPath) (entry *filer.Entry, err error) {

	data, err := store.Client.Get(ctx, string(fullpath)).Result()
	if err == redis.Nil {
		return nil, filer_pb.ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get %s : %v", fullpath, err)
	}

	entry = &filer.Entry{
		FullPath: fullpath,
	}
	err = entry.DecodeAttributesAndChunks(util.MaybeDecompressData([]byte(data)))
	if err != nil {
		return entry, fmt.Errorf("decode %s : %v", entry.FullPath, err)
	}

	return entry, nil
}

func (store *UniversalRedis2Store) DeleteEntry(ctx context.Context, fullpath util.FullPath) (err error) {

	_, err = store.Client.Del(ctx, genDirectoryListKey(string(fullpath))).Result()
	if err != nil {
		return fmt.Errorf("delete dir list %s : %v", fullpath, err)
	}

	_, err = store.Client.Del(ctx, string(fullpath)).Result()
	if err != nil {
		return fmt.Errorf("delete %s : %v", fullpath, err)
	}

	dir, name := fullpath.DirAndName()
	if store.isSuperLargeDirectory(dir) {
		return nil
	}
	if name != "" {
		_, err = store.Client.ZRem(ctx, genDirectoryListKey(dir), name).Result()
		if err != nil {
			return fmt.Errorf("DeleteEntry %s in parent dir: %v", fullpath, err)
		}
	}

	return nil
}

func (store *UniversalRedis2Store) DeleteFolderChildren(ctx context.Context, fullpath util.FullPath, limit int64) (err error) {

	if store.isSuperLargeDirectory(string(fullpath)) {
		return nil
	}

	members, err := store.Client.ZRange(ctx, genDirectoryListKey(string(fullpath)), 0, -1).Result()
	if err != nil {
		return fmt.Errorf("DeleteFolderChildren %s : %v", fullpath, err)
	}

	for _, fileName := range members {
		path := util.NewFullPath(string(fullpath), fileName)
		_, err = store.Client.Del(ctx, string(path)).Result()
		if err != nil {
			return fmt.Errorf("DeleteFolderChildren %s in parent dir: %v", fullpath, err)
		}
	}

	return nil
}

func (store *UniversalRedis2Store) ListDirectoryPrefixedEntries(ctx context.Context, dirPath util.FullPath, startFileName string, includeStartFile bool, limit int64, prefix string, eachEntryFunc filer.ListEachEntryFunc) (lastFileName string, err error) {
	return lastFileName, filer.ErrUnsupportedListDirectoryPrefixed
}

func (store *UniversalRedis2Store) ListDirectoryEntries(ctx context.Context, dirPath util.FullPath, startFileName string, includeStartFile bool, limit int64, eachEntryFunc filer.ListEachEntryFunc) (lastFileName string, err error) {

	dirListKey := genDirectoryListKey(string(dirPath))
	start := int64(0)
	if startFileName != "" {
		start, _ = store.Client.ZRank(ctx, dirListKey, startFileName).Result()
		if !includeStartFile {
			start++
		}
	}
	members, err := store.Client.ZRange(ctx, dirListKey, start, start+int64(limit)-1).Result()
	if err != nil {
		return lastFileName, fmt.Errorf("list %s : %v", dirPath, err)
	}

	// fetch entry meta
	for _, fileName := range members {
		path := util.NewFullPath(string(dirPath), fileName)
		entry, err := store.FindEntry(ctx, path)
		lastFileName = fileName
		if err != nil {
			glog.V(0).Infof("list %s : %v", path, err)
			if err == filer_pb.ErrNotFound {
				continue
			}
		} else {
			if entry.TtlSec > 0 {
				if entry.Attr.Crtime.Add(time.Duration(entry.TtlSec) * time.Second).Before(time.Now()) {
					store.Client.Del(ctx, string(path)).Result()
					store.Client.ZRem(ctx, dirListKey, fileName).Result()
					continue
				}
			}
			if !eachEntryFunc(entry) {
				break
			}
		}
	}

	return lastFileName, err
}

func genDirectoryListKey(dir string) (dirList string) {
	return dir + DIR_LIST_MARKER
}

func (store *UniversalRedis2Store) Shutdown() {
	store.Client.Close()
}
