// +build rocksdb

package rocksdb

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	weed_util "github.com/chrislusf/seaweedfs/weed/util"
	rocksdb "github.com/tecbot/gorocksdb"
	"io"
)

func init() {
	filer.Stores = append(filer.Stores, &RocksDBStore{})
}

type options struct {
	opt *rocksdb.Options
	ro  *rocksdb.ReadOptions
	wo  *rocksdb.WriteOptions
}

func (opt *options) init() {
	opt.opt = rocksdb.NewDefaultOptions()
	opt.ro = rocksdb.NewDefaultReadOptions()
	opt.wo = rocksdb.NewDefaultWriteOptions()
}

func (opt *options) close() {
	opt.opt.Destroy()
	opt.ro.Destroy()
	opt.wo.Destroy()
}

type RocksDBStore struct {
	path string
	db   *rocksdb.DB
	options
}

func (store *RocksDBStore) GetName() string {
	return "rocksdb"
}

func (store *RocksDBStore) Initialize(configuration weed_util.Configuration, prefix string) (err error) {
	dir := configuration.GetString(prefix + "dir")
	return store.initialize(dir)
}

func (store *RocksDBStore) initialize(dir string) (err error) {
	glog.Infof("filer store rocksdb dir: %s", dir)
	if err := weed_util.TestFolderWritable(dir); err != nil {
		return fmt.Errorf("Check Level Folder %s Writable: %s", dir, err)
	}
	store.options.init()
	store.opt.SetCreateIfMissing(true)
	store.db, err = rocksdb.OpenDb(store.opt, dir)

	return
}

func (store *RocksDBStore) BeginTransaction(ctx context.Context) (context.Context, error) {
	return ctx, nil
}
func (store *RocksDBStore) CommitTransaction(ctx context.Context) error {
	return nil
}
func (store *RocksDBStore) RollbackTransaction(ctx context.Context) error {
	return nil
}

func (store *RocksDBStore) InsertEntry(ctx context.Context, entry *filer.Entry) (err error) {
	dir, name := entry.DirAndName()
	key := genKey(dir, name)

	value, err := entry.EncodeAttributesAndChunks()
	if err != nil {
		return fmt.Errorf("encoding %s %+v: %v", entry.FullPath, entry.Attr, err)
	}

	err = store.db.Put(store.wo, key, value)

	if err != nil {
		return fmt.Errorf("persisting %s : %v", entry.FullPath, err)
	}

	// println("saved", entry.FullPath, "chunks", len(entry.Chunks))

	return nil
}

func (store *RocksDBStore) UpdateEntry(ctx context.Context, entry *filer.Entry) (err error) {

	return store.InsertEntry(ctx, entry)
}

func (store *RocksDBStore) FindEntry(ctx context.Context, fullpath weed_util.FullPath) (entry *filer.Entry, err error) {
	dir, name := fullpath.DirAndName()
	key := genKey(dir, name)
	data, err := store.db.Get(store.ro, key)

	if data == nil {
		return nil, filer_pb.ErrNotFound
	}
	defer data.Free()

	if err != nil {
		return nil, fmt.Errorf("get %s : %v", fullpath, err)
	}

	entry = &filer.Entry{
		FullPath: fullpath,
	}
	err = entry.DecodeAttributesAndChunks(weed_util.MaybeDecompressData(data.Data()))
	if err != nil {
		return entry, fmt.Errorf("decode %s : %v", entry.FullPath, err)
	}

	// println("read", entry.FullPath, "chunks", len(entry.Chunks), "data", len(data), string(data))

	return entry, nil
}

func (store *RocksDBStore) DeleteEntry(ctx context.Context, fullpath weed_util.FullPath) (err error) {
	dir, name := fullpath.DirAndName()
	key := genKey(dir, name)

	err = store.db.Delete(store.wo, key)
	if err != nil {
		return fmt.Errorf("delete %s : %v", fullpath, err)
	}

	return nil
}

func (store *RocksDBStore) DeleteFolderChildren(ctx context.Context, fullpath weed_util.FullPath) (err error) {
	directoryPrefix := genDirectoryKeyPrefix(fullpath, "")

	batch := rocksdb.NewWriteBatch()
	defer batch.Destroy()

	ro := rocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	ro.SetFillCache(false)

	iter := store.db.NewIterator(ro)
	defer iter.Close()
	err = enumerate(iter, directoryPrefix, nil, false, -1, func(key, value []byte) bool {
		batch.Delete(key)
		return true
	})
	if err != nil {
		return fmt.Errorf("delete list %s : %v", fullpath, err)
	}

	err = store.db.Write(store.wo, batch)

	if err != nil {
		return fmt.Errorf("delete %s : %v", fullpath, err)
	}

	return nil
}

func enumerate(iter *rocksdb.Iterator, prefix, lastKey []byte, includeLastKey bool, limit int, fn func(key, value []byte) bool) error {

	if len(lastKey) == 0 {
		iter.Seek(prefix)
	} else {
		iter.Seek(lastKey)

		if !includeLastKey {
			key := iter.Key().Data()

			if !bytes.HasPrefix(key, prefix) {
				return nil
			}

			if bytes.Equal(key, lastKey) {
				iter.Next()
			}

		}
	}

	i := 0
	for ; iter.Valid(); iter.Next() {

		if limit > 0 {
			i++
			if i > limit {
				break
			}
		}

		key := iter.Key().Data()

		if !bytes.HasPrefix(key, prefix) {
			break
		}

		ret := fn(key, iter.Value().Data())

		if !ret {
			break
		}

	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("prefix scan iterator: %v", err)
	}
	return nil
}

func (store *RocksDBStore) ListDirectoryEntries(ctx context.Context, fullpath weed_util.FullPath, startFileName string, inclusive bool,
	limit int) (entries []*filer.Entry, err error) {
	return store.ListDirectoryPrefixedEntries(ctx, fullpath, startFileName, inclusive, limit, "")
}

func (store *RocksDBStore) ListDirectoryPrefixedEntries(ctx context.Context, fullpath weed_util.FullPath, startFileName string, inclusive bool, limit int, prefix string) (entries []*filer.Entry, err error) {

	directoryPrefix := genDirectoryKeyPrefix(fullpath, prefix)
	lastFileStart := directoryPrefix
	if startFileName != "" {
		lastFileStart = genDirectoryKeyPrefix(fullpath, startFileName)
	}

	ro := rocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	ro.SetFillCache(false)

	iter := store.db.NewIterator(ro)
	defer iter.Close()
	err = enumerate(iter, directoryPrefix, lastFileStart, inclusive, limit, func(key, value []byte) bool {
		fileName := getNameFromKey(key)
		if fileName == "" {
			return true
		}
		limit--
		if limit < 0 {
			return false
		}
		entry := &filer.Entry{
			FullPath: weed_util.NewFullPath(string(fullpath), fileName),
		}

		// println("list", entry.FullPath, "chunks", len(entry.Chunks))
		if decodeErr := entry.DecodeAttributesAndChunks(weed_util.MaybeDecompressData(value)); decodeErr != nil {
			err = decodeErr
			glog.V(0).Infof("list %s : %v", entry.FullPath, err)
			return false
		}
		entries = append(entries, entry)
		return true
	})
	if err != nil {
		return entries, fmt.Errorf("prefix list %s : %v", fullpath, err)
	}

	return entries, err
}

func genKey(dirPath, fileName string) (key []byte) {
	key = hashToBytes(dirPath)
	key = append(key, []byte(fileName)...)
	return key
}

func genDirectoryKeyPrefix(fullpath weed_util.FullPath, startFileName string) (keyPrefix []byte) {
	keyPrefix = hashToBytes(string(fullpath))
	if len(startFileName) > 0 {
		keyPrefix = append(keyPrefix, []byte(startFileName)...)
	}
	return keyPrefix
}

func getNameFromKey(key []byte) string {

	return string(key[md5.Size:])

}

// hash directory, and use last byte for partitioning
func hashToBytes(dir string) []byte {
	h := md5.New()
	io.WriteString(h, dir)

	b := h.Sum(nil)

	return b
}

func (store *RocksDBStore) Shutdown() {
	store.db.Close()
	store.options.close()
}
