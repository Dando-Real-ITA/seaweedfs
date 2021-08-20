package shell

import (
	"context"
	"flag"
	"fmt"
	"github.com/chrislusf/seaweedfs/weed/filer"
	"github.com/chrislusf/seaweedfs/weed/pb/filer_pb"
	"github.com/chrislusf/seaweedfs/weed/util"
	"io"
	"path/filepath"
	"strings"
)

func init() {
	Commands = append(Commands, &commandRemoteUncache{})
}

type commandRemoteUncache struct {
}

func (c *commandRemoteUncache) Name() string {
	return "remote.uncache"
}

func (c *commandRemoteUncache) Help() string {
	return `keep the metadata but remote cache the file content for mounted directories or files

	This is designed to run regularly. So you can add it to some cronjob.
	If a file is not synchronized with the remote copy, the file will be skipped to avoid loss of data.

	remote.uncache -dir=/xxx
	remote.uncache -dir=/xxx/some/sub/dir
	remote.uncache -dir=/xxx/some/sub/dir -include=*.pdf
	remote.uncache -dir=/xxx/some/sub/dir -exclude=*.txt

`
}

func (c *commandRemoteUncache) Do(args []string, commandEnv *CommandEnv, writer io.Writer) (err error) {

	remoteUnmountCommand := flag.NewFlagSet(c.Name(), flag.ContinueOnError)

	dir := remoteUnmountCommand.String("dir", "", "a directory in filer")
	fileFiler := newFileFilter(remoteUnmountCommand)

	if err = remoteUnmountCommand.Parse(args); err != nil {
		return nil
	}

	mappings, listErr := filer.ReadMountMappings(commandEnv.option.GrpcDialOption, commandEnv.option.FilerAddress)
	if listErr != nil {
		return listErr
	}
	if *dir == "" {
		jsonPrintln(writer, mappings)
		fmt.Fprintln(writer, "need to specify '-dir' option")
		return nil
	}

	var localMountedDir string
	for k := range mappings.Mappings {
		if strings.HasPrefix(*dir, k) {
			localMountedDir = k
		}
	}
	if localMountedDir == "" {
		jsonPrintln(writer, mappings)
		fmt.Fprintf(writer, "%s is not mounted\n", *dir)
		return nil
	}

	// pull content from remote
	if err = c.uncacheContentData(commandEnv, writer, util.FullPath(*dir), fileFiler); err != nil {
		return fmt.Errorf("cache content data: %v", err)
	}

	return nil
}

func (c *commandRemoteUncache) uncacheContentData(commandEnv *CommandEnv, writer io.Writer, dirToCache util.FullPath, fileFilter *FileFilter) error {

	return recursivelyTraverseDirectory(commandEnv, dirToCache, func(dir util.FullPath, entry *filer_pb.Entry) bool {
		if !mayHaveCachedToLocal(entry) {
			return true // true means recursive traversal should continue
		}

		if fileFilter.matches(entry) {
			return true
		}

		if entry.RemoteEntry.LastLocalSyncTsNs/1e9 < entry.Attributes.Mtime {
			return true // should not uncache an entry that is not synchronized with remote
		}

		entry.RemoteEntry.LastLocalSyncTsNs = 0
		entry.Chunks = nil

		println(dir, entry.Name)

		err := commandEnv.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
			_, updateErr := client.UpdateEntry(context.Background(), &filer_pb.UpdateEntryRequest{
				Directory: string(dir),
				Entry:     entry,
			})
			return updateErr
		})
		if err != nil {
			fmt.Fprintf(writer, "uncache %+v: %v\n", dir.Child(entry.Name), err)
			return false
		}

		return true
	})
}

type FileFilter struct {
	include *string
	exclude *string
	minSize *int64
	maxSize *int64
	minAge  *int64
	maxAge  *int64
}

func newFileFilter(remoteMountCommand *flag.FlagSet) (ff *FileFilter) {
	ff = &FileFilter{}
	ff.include = remoteMountCommand.String("include", "", "pattens of file names, e.g., *.pdf, *.html, ab?d.txt")
	ff.exclude = remoteMountCommand.String("exclude", "", "pattens of file names, e.g., *.pdf, *.html, ab?d.txt")
	ff.minSize = remoteMountCommand.Int64("minSize", -1, "minimum file size in bytes")
	ff.maxSize = remoteMountCommand.Int64("maxSize", -1, "maximum file size in bytes")
	ff.minAge = remoteMountCommand.Int64("minAge", -1, "minimum file age in seconds")
	ff.maxAge = remoteMountCommand.Int64("maxAge", -1, "maximum file age in seconds")
	return
}

func (ff *FileFilter) matches(entry *filer_pb.Entry) bool {
	if *ff.include != "" {
		if ok, _ := filepath.Match(*ff.include, entry.Name); !ok {
			return true
		}
	}
	if *ff.exclude != "" {
		if ok, _ := filepath.Match(*ff.exclude, entry.Name); ok {
			return true
		}
	}
	if *ff.minSize != -1 {
		if int64(entry.Attributes.FileSize) < *ff.minSize {
			return false
		}
	}
	if *ff.maxSize != -1 {
		if int64(entry.Attributes.FileSize) > *ff.maxSize {
			return false
		}
	}
	if *ff.minAge != -1 {
		if entry.Attributes.Crtime < *ff.minAge {
			return false
		}
	}
	if *ff.maxAge != -1 {
		if entry.Attributes.Crtime > *ff.maxAge {
			return false
		}
	}
	return false
}
