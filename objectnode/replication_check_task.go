package objectnode

import (
	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/sdk/data/stream"
	"github.com/cubefs/cubefs/sdk/meta"
	"github.com/cubefs/cubefs/util/log"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultReadDirLimit = 1000
)

func NewReplicationCheckTask(masters []string, volume string, dryRun bool, currStatC chan proto.ScanStatistics, stopC chan bool) (*ReplicationCheckTask, error) {
	var err error
	var v *Volume

	metaConfig := &meta.MetaConfig{
		Volume:                  volume,
		Masters:                 masters,
		Authenticate:            false,
		ValidateOwner:           false,
		FetchVolReplicationInfo: true,
	}

	var mw *meta.MetaWrapper
	var extentClient *stream.ExtentClient
	if mw, err = meta.NewMetaWrapper(metaConfig); err != nil {
		return nil, err
	}
	extentConfig := &stream.ExtentConfig{
		Volume:            volume,
		Masters:           masters,
		FollowerRead:      true,
		OnAppendExtentKey: mw.AppendExtentKey,
		OnGetExtents:      mw.GetExtents,
	}

	if extentClient, err = stream.NewExtentClient(extentConfig); err != nil {
		log.LogErrorf("NewFailedReplicationScanner: new extent client failed: volume(%v) err(%v)", volume, err)
		return nil, err
	}

	vm := NewVolumeManager(masters, true)
	if v, err = vm.Volume(volume); err != nil {
		return nil, err
	}

	task := &ReplicationCheckTask{
		vm:           vm,
		Volume:       v,
		mw:           mw,
		extentClient: extentClient,
		C:            make(chan *proto.ScanDentry, 10000),
		statistics:   &proto.ScanStatistics{},
		startTime:    time.Now(),
		currStatC:    currStatC,
		stopC:        stopC,
		dryRun:       dryRun,
	}

	return task, nil
}

type ReplicationCheckTask struct {
	Volume *Volume
	vm     *VolumeManager

	mw           *meta.MetaWrapper
	extentClient *stream.ExtentClient

	C chan *proto.ScanDentry

	statistics *proto.ScanStatistics
	startTime  time.Time

	currStatC chan proto.ScanStatistics
	stopC     chan bool

	dryRun bool
}

func (t *ReplicationCheckTask) findPrefixInode() (inode uint64, prefixDirs []string, err error) {
	prefix := t.GetScanStartPrefix()
	prefixDirs = make([]string, 0)

	var dirs []string
	if prefix != "" {
		dirs = strings.Split(prefix, "/")
		log.LogInfof("FindPrefixInode: volume(%v), prefix(%v), dirs(%v), len(%v)", t.Volume, prefix, dirs, len(dirs))
	}
	if len(dirs) <= 1 {
		return proto.RootIno, prefixDirs, nil
	}

	parentId := proto.RootIno
	for index, dir := range dirs {

		// Because lookup can only retrieve dentry whose name exactly matches,
		// so do not lookup the last part.
		if index+1 == len(dirs) {
			break
		}

		curIno, curMode, err := t.mw.Lookup_ll(parentId, dir)

		// If the part except the last part does not match exactly the same dentry, there is
		// no path matching the path prefix. An ENOENT error is returned to the caller.
		if err == syscall.ENOENT {
			log.LogErrorf("FindPrefixInode: find directories fail ENOENT: parentId(%v) dir(%v)", parentId, dir)
			return 0, nil, syscall.ENOENT
		}

		if err != nil && err != syscall.ENOENT {
			log.LogErrorf("FindPrefixInode: find directories fail: prefix(%v) err(%v)", prefix, err)
			return 0, nil, err
		}

		// Because the file cannot have the next level members,
		// if there is a directory in the middle of the prefix,
		// it means that there is no file matching the prefix.
		if !os.FileMode(curMode).IsDir() {
			return 0, nil, syscall.ENOENT
		}

		prefixDirs = append(prefixDirs, dir)
		parentId = curIno
	}
	inode = parentId

	return
}

func (t *ReplicationCheckTask) Start() (err error) {
	var firstDentry *proto.ScanDentry
	parentId, prefixDirs, err := t.findPrefixInode()
	if err != nil {
		return
	}

	var currentPath string
	if len(prefixDirs) > 0 {
		currentPath = strings.Join(prefixDirs, pathSep)
	}

	// 2. check object replication
	firstDentry = &proto.ScanDentry{
		Inode: parentId,
		Path:  strings.TrimPrefix(currentPath, pathSep),
		Type:  uint32(os.ModeDir),
	}

	prefix := t.GetScanStartPrefix()
	// traverse directory
	go func() {
		res := t.startHeal(firstDentry, prefix)
		t.statistics.TraverseDone = true
		if res {
			t.statistics.TraverseStatus = "success"
		} else {
			t.statistics.TraverseStatus = "failed"
		}
		return
	}()

	// handle file
	go t.handle()

	go t.updateStat()

	return
}

func (t *ReplicationCheckTask) Stop() {
	t.mw.Close()
	t.extentClient.Close()
}

func (t *ReplicationCheckTask) updateStat() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopC:
			return
		case <-ticker.C:
			t.currStatC <- *t.statistics
		}
	}
}

func (t *ReplicationCheckTask) traverse(dentry *proto.ScanDentry, prefix string) bool {
	atomic.AddInt64(&t.statistics.DirScannedNum, 1)
	atomic.AddInt64(&t.statistics.TotalInodeScannedNum, 1)
	marker := ""
	done := false
	for !done {
		children, err := t.mw.ReadDirLimit_ll(dentry.Inode, marker, defaultReadDirLimit)
		if err != nil && err != syscall.ENOENT {
			atomic.AddInt64(&t.statistics.ErrorSkippedNum, 1)
			return false
		}
		if err == syscall.ENOENT {
			break
		}

		if marker != "" {
			if len(children) == 1 && marker == children[0].Name {
				break
			}
			children = children[1:]
		}

		for _, child := range children {
			childDentry := &proto.ScanDentry{
				ParentId: dentry.Inode,
				Name:     child.Name,
				Inode:    child.Inode,
				Path:     strings.TrimPrefix(dentry.Path+pathSep+child.Name, pathSep),
				Type:     child.Type,
			}
			if !strings.HasPrefix(childDentry.Path, prefix) {
				continue
			}

			if !os.FileMode(childDentry.Type).IsDir() {
				select {
				case t.C <- childDentry:
					// write file to channel
				}
			} else {
				if !t.traverse(childDentry, prefix) {
					return false
				}
			}
		}

		childrenNr := len(children)
		if (marker == "" && childrenNr < defaultReadDirLimit) || (marker != "" && childrenNr+1 < defaultReadDirLimit) {
			done = true
		} else {
			marker = children[childrenNr-1].Name
		}
	}

	return true // traverse all the child successfully
}

func (t *ReplicationCheckTask) handle() {
	t.statistics.FileHandleStatus = "success"
	for {
		select {
		case file := <-t.C:
			if file != nil {
				//fmt.Printf("handle file %v \n", file.Path)
				if err := t.handleFile(file); err != nil {
					t.statistics.FileHandleStatus = "failed"
				}
			}
		default:
			// all the dirs has benn traversed , files has been handled
			if t.statistics.TraverseDone && len(t.C) == 0 {
				t.statistics.Done = true
				return
			}
			time.Sleep(3 * time.Second)
		}
	}
}

func (t *ReplicationCheckTask) handleFile(dentry *proto.ScanDentry) (err error) {
	var attrInfo *proto.XAttrInfo
	var inodeInfo *proto.InodeInfo

	atomic.AddInt64(&t.statistics.FileScannedNum, 1)
	atomic.AddInt64(&t.statistics.TotalInodeScannedNum, 1)

	if attrInfo, err = t.mw.XAttrGetAll_ll(dentry.Inode); err != nil {
		return err
	}

	if targetIds, _ := t.Volume.shouldObjectReplicated(dentry.Path, attrInfo.XAttrs[VolumeReplicationStatus]); len(targetIds) > 0 {
		atomic.AddInt64(&t.statistics.FailedObjectsDetected, 1)

		inodeInfo, err = t.Volume.mw.InodeGet_ll(dentry.Inode)
		if err != nil {
			log.LogErrorf("FailedReplicationScanner.BatchHandleFiles: meta get inode info failed : volume(%v) path(%v) inode(%v) err(%v)", t.Volume, dentry.Path, dentry.Inode, err)
			return err
		}

		if t.dryRun {
			return nil
		}

		if err = t.Volume.replicateObject(dentry.Path, inodeInfo.Inode, inodeInfo.Size, inodeInfo.CreateTime.Unix(), attrInfo.XAttrs, targetIds); err != nil {
			return err
		}
		atomic.AddInt64(&t.statistics.FailedObjectsHealed, 1)
	}

	return nil
}

func (t *ReplicationCheckTask) GetScanStartPrefix() (commonPrefix string) {
	prefixes := t.mw.GetPrefixes()
	if len(prefixes) == 0 {
		return
	}

	return prefixes[0]
}

func (t *ReplicationCheckTask) tryHealDeletion() {
	var deletedDentries []*proto.DeletedDentryInfo
	var err error
	if deletedDentries, err = t.mw.ListAllDeletedDentries(); err != nil {
		return
	}
	atomic.AddInt64(&t.statistics.FailedDeletion, int64(len(deletedDentries)))
	if !t.dryRun {
		for _, dentry := range deletedDentries {
			if targetIds, _ := t.mw.ShouldObjectReplicated(dentry.Path, ""); len(targetIds) > 0 {
				if err = ReplicateDeletion(t.mw, dentry.Inode, t.Volume.name, dentry.Path, targetIds, dentry.Time); err == nil {
					// deletion replicated successfully
					atomic.AddInt64(&t.statistics.FailedDeletionHealed, 1)
				}
			}
		}
	}

}

func (t *ReplicationCheckTask) startHeal(dentry *proto.ScanDentry, prefix string) bool {
	// check deletion replication
	t.tryHealDeletion()
	// check object replication status
	return t.traverse(dentry, prefix)
}
