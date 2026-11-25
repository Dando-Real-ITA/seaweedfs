package command

import (
	weed_server "github.com/seaweedfs/seaweedfs/weed/server"
	flag "github.com/seaweedfs/seaweedfs/weed/util/fla9"
)

const (
	defaultDownloadCacheCapacityMB          = 16384
	defaultDownloadStagingMemoryThresholdMB = 16
	defaultDownloadStagingMemoryMB          = 1024
	defaultDownloadStagingDiskMB            = 16384
)

type filerDownloadCacheOptions struct {
	cacheDir                 *string
	cacheCapacityMB          *int64
	spoolDir                 *string
	stagingMemoryThresholdMB *int64
	stagingMemoryMB          *int64
	stagingDiskMB            *int64
}

func (o *filerDownloadCacheOptions) registerFlags(fs *flag.FlagSet, prefix string) {
	o.cacheDir = fs.String(prefix+"cacheDir", "", "local HTTP download chunk cache directory (empty disables retention; do not share with a mount cache)")
	o.cacheCapacityMB = fs.Int64(prefix+"cacheCapacityMB", defaultDownloadCacheCapacityMB, "HTTP download disk chunk cache capacity in MB, when "+prefix+"cacheDir is set (0 disables retention)")
	o.spoolDir = fs.String(prefix+"cacheDirWrite", "", "HTTP download staging directory (defaults to a private directory under "+prefix+"cacheDir, or the OS temporary directory)")
	o.stagingMemoryThresholdMB = fs.Int64(prefix+"downloadStagingMemoryThresholdMB", defaultDownloadStagingMemoryThresholdMB, "HTTP download chunks up to this size in MB are staged in memory, larger ones on disk")
	o.stagingMemoryMB = fs.Int64(prefix+"downloadStagingMemoryMB", defaultDownloadStagingMemoryMB, "total memory in MB for staging HTTP download chunks")
	o.stagingDiskMB = fs.Int64(prefix+"downloadStagingDiskMB", defaultDownloadStagingDiskMB, "total disk space in MB for staging HTTP download chunks; also the largest chunk size that can be staged")
}

// serverOption tolerates commands that embed a filer without registering these flags.
func (o *filerDownloadCacheOptions) serverOption() weed_server.DownloadCacheOption {
	opt := weed_server.DownloadCacheOption{
		DownloadStagingMemoryThresholdMB: defaultDownloadStagingMemoryThresholdMB,
		DownloadStagingMemoryMB:          defaultDownloadStagingMemoryMB,
		DownloadStagingDiskMB:            defaultDownloadStagingDiskMB,
	}
	if o.cacheDir != nil {
		opt.DownloadCacheDir = *o.cacheDir
	}
	if o.cacheCapacityMB != nil {
		opt.DownloadCacheCapacityMB = *o.cacheCapacityMB
	}
	if o.spoolDir != nil {
		opt.DownloadSpoolDir = *o.spoolDir
	}
	if o.stagingMemoryThresholdMB != nil {
		opt.DownloadStagingMemoryThresholdMB = *o.stagingMemoryThresholdMB
	}
	if o.stagingMemoryMB != nil {
		opt.DownloadStagingMemoryMB = *o.stagingMemoryMB
	}
	if o.stagingDiskMB != nil {
		opt.DownloadStagingDiskMB = *o.stagingDiskMB
	}
	return opt
}
