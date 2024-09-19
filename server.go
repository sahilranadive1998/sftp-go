package sftp

// sftp server counterpart

import (
	"encoding"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rdleal/intervalst/interval"
	"github.com/vmware/go-nfs-client/nfs"
	"github.com/vmware/go-nfs-client/nfs/rpc"
)

const (
	// SftpServerWorkerCount defines the number of workers for the SFTP server
	SftpServerWorkerCount = 8
)

// Server is an SSH File Transfer Protocol (sftp) server.
// This is intended to provide the sftp subsystem to an ssh server daemon.
// This implementation currently supports most of sftp server protocol version 3,
// as specified at https://filezilla-project.org/specs/draft-ietf-secsh-filexfer-02.txt

type treeEntry struct {
	startOffset int64
	endOffset   int64
	data        []byte
}

// var wg2 sync.WaitGroup

type Server struct {
	*serverConn
	debugStream   io.Writer
	readOnly      bool
	pktMgr        *packetManager
	openFiles     map[string]*os.File
	nfsFile       *nfs.File
	nfsTarget     *nfs.Target
	nfsFileSize   int64
	openFilesLock sync.RWMutex
	handleCount   int
	workDir       string
	maxTxPacket   uint32
	cache         *interval.SearchTree[treeEntry, int64]
	count         int
	wg2           sync.WaitGroup
}

func (svr *Server) nextHandle(f *os.File) string {
	svr.openFilesLock.Lock()
	defer svr.openFilesLock.Unlock()
	svr.handleCount++
	handle := strconv.Itoa(svr.handleCount)
	svr.openFiles[handle] = f
	return handle
}

func (svr *Server) nextHandleNfs() string {
	svr.openFilesLock.Lock()
	defer svr.openFilesLock.Unlock()
	svr.handleCount++
	handle := strconv.Itoa(svr.handleCount)
	// svr.openFiles[handle] = f
	return handle
}

func (svr *Server) closeHandle(handle string) error {
	svr.openFilesLock.Lock()
	defer svr.openFilesLock.Unlock()
	if f, ok := svr.openFiles[handle]; ok {
		delete(svr.openFiles, handle)
		return f.Close()
	}

	return EBADF
}

func (svr *Server) getHandle(handle string) (*os.File, bool) {
	svr.openFilesLock.RLock()
	defer svr.openFilesLock.RUnlock()
	f, ok := svr.openFiles[handle]
	return f, ok
}

type serverRespondablePacket interface {
	encoding.BinaryUnmarshaler
	id() uint32
	respond(svr *Server) responsePacket
}

// NewServer creates a new Server instance around the provided streams, serving
// content from the root of the filesystem.  Optionally, ServerOption
// functions may be specified to further configure the Server.
//
// A subsequent call to Serve() is required to begin serving files over SFTP.
func NewServer(rwc io.ReadWriteCloser, options ...ServerOption) (*Server, error) {
	svrConn := &serverConn{
		conn: conn{
			Reader:      rwc,
			WriteCloser: rwc,
		},
	}
	s := &Server{
		serverConn:  svrConn,
		debugStream: ioutil.Discard,
		pktMgr:      newPktMgr(svrConn),
		openFiles:   make(map[string]*os.File),
		maxTxPacket: defaultMaxTxPacket,
		count:       1,
	}

	for _, o := range options {
		if err := o(s); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// A ServerOption is a function which applies configuration to a Server.
type ServerOption func(*Server) error

// WithDebug enables Server debugging output to the supplied io.Writer.
func WithDebug(w io.Writer) ServerOption {
	return func(s *Server) error {
		s.debugStream = w
		return nil
	}
}

// ReadOnly configures a Server to serve files in read-only mode.
func ReadOnly() ServerOption {
	return func(s *Server) error {
		s.readOnly = true
		return nil
	}
}

// WithAllocator enable the allocator.
// After processing a packet we keep in memory the allocated slices
// and we reuse them for new packets.
// The allocator is experimental
func WithAllocator() ServerOption {
	return func(s *Server) error {
		alloc := newAllocator()
		s.pktMgr.alloc = alloc
		s.conn.alloc = alloc
		return nil
	}
}

// WithServerWorkingDirectory sets a working directory to use as base
// for relative paths.
// If unset the default is current working directory (os.Getwd).
func WithServerWorkingDirectory(workDir string) ServerOption {
	return func(s *Server) error {
		s.workDir = cleanPath(workDir)
		return nil
	}
}

// WithMaxTxPacket sets the maximum size of the payload returned to the client,
// measured in bytes. The default value is 32768 bytes, and this option
// can only be used to increase it. Setting this option to a larger value
// should be safe, because the client decides the size of the requested payload.
//
// The default maximum packet size is 32768 bytes.
func WithMaxTxPacket(size uint32) ServerOption {
	return func(s *Server) error {
		if size < defaultMaxTxPacket {
			return errors.New("size must be greater than or equal to 32768")
		}

		s.maxTxPacket = size

		return nil
	}
}

type rxPacket struct {
	pktType  fxp
	pktBytes []byte
}

// Up to N parallel servers
func (svr *Server) sftpServerWorker(pktChan chan orderedRequest) error {
	for pkt := range pktChan {
		start := time.Now()
		// readonly checks
		readonly := true
		switch pkt := pkt.requestPacket.(type) {
		case notReadOnly:
			readonly = false
		case *sshFxpOpenPacket:
			readonly = pkt.readonly()
			fmt.Fprintf(svr.debugStream, "sftp open packet is readonly:%v\n", readonly)
		case *sshFxpExtendedPacket:
			readonly = pkt.readonly()
		}

		// If server is operating read-only and a write operation is requested,
		// return permission denied
		if !readonly && svr.readOnly {
			svr.pktMgr.readyPacket(
				svr.pktMgr.newOrderedResponse(statusFromError(pkt.id(), syscall.EPERM), pkt.orderID()),
			)
			continue
		}

		if err := handlePacket(svr, pkt); err != nil {
			return err
		}
		fmt.Fprintf(svr.debugStream, "time taken to process packet: %v\n", time.Since(start))
	}
	return nil
}

// func nfsManager(f *nfs.File) {
// 	defer wg.Done()
// 	// Loop:
// 	for {
// 		if queue.Size() == 0 {
// 			continue
// 		}
// 		item := queue.Dequeue()

// 		if item.offset == -1 {
// 			break
// 		}
// 		if item.length != 0 {
// 			data := make([]byte, item.length, item.length+13)
// 			f.Seek(item.offset, io.SeekStart)
// 			f.Read(data)
// 			// fmt.Fprintf(s.debugStream, "Read data 1 at offset: %d, of length %d is:%v\n", item.offset, len(data), data)
// 			readData = data
// 		} else {
// 			// fmt.Fprintf(s.debugStream, "Write data at offset: %d, of length %d is:%v\n", item.offset, len(*item.data), *item.data)
// 			f.Seek(item.offset, io.SeekStart)
// 			f.Write(*item.data)
// 		}

// 		// switch item.(type) {
// 		// case int:
// 		// 	break Loop
// 		// default:
// 		// 	// Do the thing here

// 		// }
// 		// if item == nil {
// 		//     // Exit when the queue is empty
// 		//     break
// 		// }
// 		// fmt.Println("Consumer", "Dequeue:", item)
// 		// time.Sleep(time.Millisecond * 150) // Simulate some work
// 	}
// }

func handlePacket(s *Server, p orderedRequest) error {
	var rpkt responsePacket
	// fmt.Fprintf(s.debugStream, "sftp request packet type %v\n", p.requestPacket)
	orderID := p.orderID()
	switch p := p.requestPacket.(type) {
	case *sshFxInitPacket:
		fmt.Fprintf(s.debugStream, "sftp init packet. Setup NFS connection here\n")
		// We setup the NFS connection here. Later we can parse the url
		// host := "127.0.0.1"
		// target := "/default-container-17246812063770"

		// mount, err := nfs.DialMount(host)
		// if err != nil {
		// 	fmt.Fprint(s.debugStream, err.Error())
		// }
		// // defer mount.Close()

		// hostname, _ := os.Hostname()
		// auth := rpc.NewAuthUnix(hostname, 1000, 1000)

		// v, err := mount.Mount(target, auth.Auth())
		// if err != nil {
		// 	fmt.Fprint(s.debugStream, err.Error())
		// }
		// // defer v.Close()

		// s.nfsTarget = v

		cmpFn := func(x, y int64) int {
			if x-y > 0 {
				return 1
			} else if x-y == 0 {
				return 0
			}
			return -1
		}
		s.cache = interval.NewSearchTree[treeEntry](cmpFn)

		rpkt = &sshFxVersionPacket{
			Version:    sftpProtocolVersion,
			Extensions: sftpExtensions,
		}
	case *sshFxpStatPacket:
		// stat the requested file
		fmt.Fprintf(s.debugStream, "sftp stat packet\n")

		info, err := os.Stat("/mnt/nutanix" + s.toLocalPath(p.Path))
		rpkt = &sshFxpStatResponse{
			ID:   p.ID,
			info: info,
		}
		if err != nil {
			rpkt = statusFromError(p.ID, err)
		}
	case *sshFxpLstatPacket:
		// stat the requested file
		fmt.Fprintf(s.debugStream, "sftp lstat packet: %v\n", s.toLocalPath(p.Path))
		info, err := os.Lstat("/mnt/nutanix" + s.toLocalPath(p.Path))
		rpkt = &sshFxpStatResponse{
			ID:   p.ID,
			info: info,
		}
		if err != nil {
			rpkt = statusFromError(p.ID, err)
		}
	case *sshFxpFstatPacket:
		fmt.Fprintf(s.debugStream, "sftp fstat packet, packet id:%v\n", p.id())
		var packetId uint32

		packetId = p.id()
		// info2, err := s.nfsFile.FSInfo()
		// fmt.Fprintf(s.debugStream, "Opened nfs file stats: %v\n", info2)
		// f, _ := s.getHandle(p.Handle)
		// var err error = EBADF
		var info os.FileInfo
		// if err == nil {
		info = MyFileInfo{size: int64(s.nfsFileSize), mode: 0666, isDir: false}
		// info, err = f.Stat()
		fmt.Fprintf(s.debugStream, "Opened file stats: %v\n", info)
		rpkt = &sshFxpStatResponse{
			ID:   packetId,
			info: info,
		}
		// }
		// if err != nil {
		// 	rpkt = statusFromError(p.ID, err)
		// }
	case *sshFxpMkdirPacket:
		// TODO FIXME: ignore flags field
		err := os.Mkdir(s.toLocalPath(p.Path), 0o755)
		rpkt = statusFromError(p.ID, err)
	case *sshFxpRmdirPacket:
		err := os.Remove(s.toLocalPath(p.Path))
		rpkt = statusFromError(p.ID, err)
	case *sshFxpRemovePacket:
		err := os.Remove(s.toLocalPath(p.Filename))
		rpkt = statusFromError(p.ID, err)
	case *sshFxpRenamePacket:
		err := os.Rename(s.toLocalPath(p.Oldpath), s.toLocalPath(p.Newpath))
		rpkt = statusFromError(p.ID, err)
	case *sshFxpSymlinkPacket:
		err := os.Symlink(s.toLocalPath(p.Targetpath), s.toLocalPath(p.Linkpath))
		rpkt = statusFromError(p.ID, err)
	case *sshFxpClosePacket:
		fmt.Fprintf(s.debugStream, "sftp close packet\n")
		fmt.Printf("Cache: %d\n", s.cache.Size())
		for s.evictMinEntry() {
			// treeEntry, ok := s.cache.Min()
			// if !ok {
			// 	fmt.Println("Breaking")
			// 	break
			// }
			// s.wg2.Wait()
			// s.wg2.Add(1)
			// s.flushToNfs(treeEntry.startOffset, treeEntry.data)
			// s.cache.Delete(treeEntry.startOffset, treeEntry.endOffset)
		}
		s.wg2.Wait()
		// emptySlice := make([]byte, 0)
		// queue.Enqueue(&Data{-1, 0, &emptySlice})
		// wg.Wait()
		s.nfsFile.Close()
		rpkt = statusFromError(p.ID, s.closeHandle(p.Handle))
	case *sshFxpReadlinkPacket:
		f, err := os.Readlink(s.toLocalPath(p.Path))
		rpkt = &sshFxpNamePacket{
			ID: p.ID,
			NameAttrs: []*sshFxpNameAttr{
				{
					Name:     f,
					LongName: f,
					Attrs:    emptyFileStat,
				},
			},
		}
		if err != nil {
			rpkt = statusFromError(p.ID, err)
		}
	case *sshFxpRealpathPacket:
		fmt.Fprintf(s.debugStream, "sftp realpath packet\n")
		f, err := filepath.Abs(s.toLocalPath(p.Path))
		// fmt.Fprintf(s.debugStream, "sftp realpath is: %v\n", f)
		f = cleanPath(f)
		f = "/"
		fmt.Fprintf(s.debugStream, "sftp realpath cleanpath is: %v\n", f)
		rpkt = &sshFxpNamePacket{
			ID: p.ID,
			NameAttrs: []*sshFxpNameAttr{
				{
					Name:     f,
					LongName: f,
					Attrs:    emptyFileStat,
				},
			},
		}
		if err != nil {
			rpkt = statusFromError(p.ID, err)
		}
	case *sshFxpOpendirPacket:
		lp := s.toLocalPath(p.Path)

		if stat, err := os.Stat(lp); err != nil {
			rpkt = statusFromError(p.ID, err)
		} else if !stat.IsDir() {
			rpkt = statusFromError(p.ID, &os.PathError{
				Path: lp, Err: syscall.ENOTDIR,
			})
		} else {
			rpkt = (&sshFxpOpenPacket{
				ID:     p.ID,
				Path:   p.Path,
				Pflags: sshFxfRead,
			}).respond(s)
		}
	case *sshFxpReadPacket:
		// fmt.Printf("sftp read packet for offset: %d of length: %d\n", int(p.Offset), p.Len)
		// fmt.Fprintf(s.debugStream, "sftp read packet \n")
		var err error = EBADF
		// f, ok := s.getHandle(p.Handle)
		// if ok {
		// 	err = nil
		// 	data := p.getDataSlice(s.pktMgr.alloc, orderID, s.maxTxPacket)
		// 	n, _err := f.ReadAt(data, int64(p.Offset))
		// 	if _err != nil && (_err != io.EOF || n == 0) {
		// 		err = _err
		// 	}
		// 	rpkt = &sshFxpDataPacket{
		// 		ID:     p.ID,
		// 		Length: uint32(n),
		// 		Data:   data[:n],
		// 		// do not use data[:n:n] here to clamp the capacity, we allocated extra capacity above to avoid reallocations
		// 	}
		// }
		start := time.Now()
		err = nil
		// p already has the length of read which is being used
		var data []byte
		data, found := s.readBeforeNfs(int64(p.Offset), int(p.Len))

		if !found {
			s.wg2.Wait()
			data = p.getDataSlice(s.pktMgr.alloc, orderID, s.maxTxPacket)
			s.nfsFile.Seek(int64(p.Offset), io.SeekStart)
			n, _err := s.nfsFile.Read(data)
			// for {
			// 	if queue.Size() == 0 && readData != nil {
			// 		break
			// 	}
			// }
			// s.nfsFile.Seek(int64(p.Offset), io.SeekStart)
			// n, _err := s.nfsFile.Read(data)
			fmt.Fprintf(s.debugStream, "NFS,read:%v\n", time.Since(start).Nanoseconds())
			start = time.Now()
			data = s.replaceReadData(int64(p.Offset), int(p.Len), data)
			// fmt.Fprintf(s.debugStream, "Read data at offset: %d, of length %d is:%v\n", p.Offset, len(s.readData), s.readData)
			fmt.Fprintf(s.debugStream, "Cache,read:%v\n", time.Since(start).Nanoseconds())
			if n == 0 {
				n = len(data)
			}
			if _err != nil && (_err != io.EOF || n == 0) {
				err = _err
			}
		}

		// fmt.Printf("Read length: %d\n", n)

		// if p.Len != 16384 || s.count != 0 {
		// 	s.count -= 1
		// 	fmt.Printf("Read data: %v\n", data[:n])
		// }

		// n, _err := f.ReadAt(data, int64(p.Offset))

		rpkt = &sshFxpDataPacket{
			ID:     p.ID,
			Length: uint32(len(data)),
			Data:   data[:len(data)],
			// do not use data[:n:n] here to clamp the capacity, we allocated extra capacity above to avoid reallocations
		}

		if err != nil {
			fmt.Println("In Error")
			rpkt = statusFromError(p.ID, err)
		}

	case *sshFxpWritePacket:
		// fmt.Printf("Received write request on sftp server for offset : %d, data length: %d\n", p.Offset, len(p.Data))
		// fmt.Printf("Incoming Write: %v\n", p.Data)
		// fmt.Fprintf(s.debugStream, "sftp write packet \n")
		s.writeToCache(int64(p.Offset), p.Data)

		// s.nfsFile.Seek(int64(p.Offset), io.SeekStart)
		// _, err := s.nfsFile.Write(p.Data)

		// f, ok := s.getHandle(p.Handle)
		// var err error = EBADF
		// if ok {
		// 	_, err = f.WriteAt(p.Data, int64(p.Offset))
		// }
		rpkt = statusFromError(p.ID, nil)
	case *sshFxpExtendedPacket:
		if p.SpecificPacket == nil {
			rpkt = statusFromError(p.ID, ErrSSHFxOpUnsupported)
		} else {
			rpkt = p.respond(s)
		}
	case *sshFxpOpenPacket:
		fmt.Fprintf(s.debugStream, "sftp open packet\n")
		rpkt = p.respond(s)
		fmt.Fprintf(s.debugStream, "Open file handle: %v\n", s.nfsFile)
		// fmt.Fprintf(s.debugStream, "Open file attr: %v\n", s.nfsFile.FsInfoFile)
		// fmt.Fprintf(s.debugStream, "Open target file attr: %v\n", s.nfsTarget.Fsinfo)
	case serverRespondablePacket:
		fmt.Fprintf(s.debugStream, "sftp respondable packet\n")
		rpkt = p.respond(s)
	default:
		return fmt.Errorf("unexpected packet type %T", p)
	}
	// fmt.Fprintf(s.debugStream, "sftp response order id: %v\n", rpkt)
	// fmt.Fprintf(s.debugStream, "sftp response packet id: %v and orderID is: %v\n", rpkt.id(), orderID)
	s.pktMgr.readyPacket(s.pktMgr.newOrderedResponse(rpkt, orderID))
	return nil
}

func (svr *Server) readBeforeNfs(startOffset int64, length int) ([]byte, bool) {
	endOffset := startOffset + int64(length)
	treeEntries, _ := svr.cache.AllIntersections(startOffset, endOffset)
	var data []byte
	if len(treeEntries) == 1 {
		entryStartOffset := treeEntries[0].startOffset
		entryEndOffset := treeEntries[0].endOffset

		if entryStartOffset <= startOffset && entryEndOffset >= endOffset {
			data = make([]byte, length)
			copy(data, treeEntries[0].data[(startOffset-entryStartOffset):(startOffset-entryStartOffset+int64(length))])
			return data, true
		}

	}
	return data, false
}

func (svr *Server) replaceReadData(startOffset int64, length int, data []byte) []byte {
	endOffset := startOffset + int64(length)
	// fmt.Printf("startOffset: %d, endOffset: %d\n", startOffset, endOffset)
	treeEntries, ok := svr.cache.AllIntersections(startOffset, endOffset)

	if len(data) == 0 {
		data = make([]byte, length)
	}
	if ok {
		// fmt.Printf("Found overlapping entry\n")
		for _, treeEntry := range treeEntries {
			entryStartOffset := treeEntry.startOffset
			entryEndOffset := treeEntry.endOffset
			// fmt.Printf("entryStartOffset: %d, entryEndOffset: %d\n", entryStartOffset, entryEndOffset)

			if startOffset <= entryStartOffset && entryStartOffset < startOffset+int64(length) {
				if entryEndOffset >= endOffset {
					// fmt.Println("In case 1")
					data = append(data[:(entryStartOffset-startOffset)], treeEntry.data[:(endOffset-entryStartOffset)]...)
				} else {
					// fmt.Println("In case 2")
					data = append(data[:(entryStartOffset-startOffset)], append(treeEntry.data, data[(entryEndOffset-startOffset):]...)...)
				}
			} else if entryStartOffset < startOffset && entryEndOffset > startOffset {
				if entryEndOffset <= endOffset {
					// fmt.Println("In case 3")
					data = append(treeEntry.data[(startOffset-entryStartOffset):], data[(entryEndOffset-startOffset):]...)
				} else {
					// fmt.Println("In case 4")
					data = treeEntry.data[(startOffset - entryStartOffset) : (startOffset-entryStartOffset)+int64(length)]
				}

			}
		}
	}
	return data
}

func (svr *Server) writeToCache(startOffset int64, data []byte) {
	start := time.Now()
	endOffset := startOffset + int64(len(data))
	treeEntries, ok := svr.cache.AllIntersections(startOffset, endOffset)
	// treeEntriesToDelete
	var newData []byte
	newData = data
	// fmt.Printf("Found %d intersections with offset: %d \n", len(treeEntries), startOffset)
	// fmt.Printf("Write data: %v\n", data)
	if ok {
		for _, treeEntry := range treeEntries {
			entryStartOffset := treeEntry.startOffset
			entryEndOffset := treeEntry.endOffset
			// fmt.Printf("Original data length: %d\n", len(treeEntry.data))
			// fmt.Printf("Original data: %v\n", treeEntry.data)
			if startOffset >= entryStartOffset && endOffset <= entryEndOffset {
				updatedData := append(treeEntry.data[:(startOffset-entryStartOffset)], append(data, treeEntry.data[(endOffset-entryStartOffset):]...)...)
				treeEntry.data = updatedData
				// fmt.Printf("Updated treeEntry data: %v\n", treeEntry.data)
				svr.cache.Insert(entryStartOffset, entryEndOffset, treeEntry)
				fmt.Fprintf(svr.debugStream, "Cache,overwrite:%v\n", time.Since(start).Nanoseconds())
				return
			} else if entryStartOffset >= startOffset && entryEndOffset <= endOffset {
				svr.cache.Delete(entryStartOffset, entryEndOffset)
			} else if entryStartOffset < startOffset && entryEndOffset >= startOffset && entryEndOffset <= endOffset {
				newData = append(treeEntry.data[:(startOffset-entryStartOffset)], data...)
				startOffset = entryStartOffset
				svr.cache.Delete(entryStartOffset, entryEndOffset)
			} else if entryStartOffset >= startOffset && entryStartOffset <= endOffset && entryEndOffset > endOffset {
				newData = append(data, treeEntry.data[(endOffset-entryStartOffset):]...)
				svr.cache.Delete(entryStartOffset, entryEndOffset)
			}
		}
	}
	// fmt.Printf("Length of data is %d \n", len(newData))
	// fmt.Printf("start offset: %d, end offset: %d\n", startOffset, endOffset)
	// fmt.Printf("Write data: %v\n", data)
	if len(newData) >= 4*1024 {
		svr.wg2.Wait()
		svr.wg2.Add(1)
		svr.flushToNfs(startOffset, newData)
		fmt.Fprintf(svr.debugStream, "NFS,write:%v\n", time.Since(start).Nanoseconds())
	} else {
		// fmt.Println("Inserting")

		// fmt.Printf("cache size: %v\n", svr.cache.Size())
		if svr.cache.Size() == 128 {
			_ = svr.evictMinEntry()
		}
		t := new(treeEntry)
		t.startOffset = startOffset
		t.endOffset = endOffset
		t.data = make([]byte, len(newData))
		copy(t.data, newData)
		// fmt.Printf("data: %v\n", t.data)
		svr.cache.Insert(startOffset, endOffset, *t)
		fmt.Fprintf(svr.debugStream, "Cache,write:%v\n", time.Since(start).Nanoseconds())
	}
}

func (svr *Server) flushToNfs(writeOffset int64, data []byte) {
	defer svr.wg2.Done()
	// fmt.Printf("Flushing offset: %d , data length: %d\n", writeOffset, len(data))
	// fmt.Printf("data: %v\n", data)
	svr.nfsFile.Seek(writeOffset, io.SeekStart)
	svr.nfsFile.Write(data)
}

func (svr *Server) evictMinEntry() bool {
	treeEntry, ok := svr.cache.Min()
	if !ok {
		fmt.Println("Cache is empty")
		return false
	}
	svr.wg2.Wait()
	svr.wg2.Add(1)
	svr.flushToNfs(treeEntry.startOffset, treeEntry.data)
	svr.cache.Delete(treeEntry.startOffset, treeEntry.endOffset)
	return true
}

// Serve serves SFTP connections until the streams stop or the SFTP subsystem
// is stopped. It returns nil if the server exits cleanly.
func (svr *Server) Serve() error {
	defer func() {
		if svr.pktMgr.alloc != nil {
			svr.pktMgr.alloc.Free()
		}
	}()
	var wg sync.WaitGroup
	runWorker := func(ch chan orderedRequest) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svr.sftpServerWorker(ch); err != nil {
				svr.conn.Close() // shuts down recvPacket
			}
		}()
	}
	pktChan := svr.pktMgr.workerChan(runWorker)

	var err error
	var pkt requestPacket
	var pktType uint8
	var pktBytes []byte
	for {
		pktType, pktBytes, err = svr.serverConn.recvPacket(svr.pktMgr.getNextOrderID())
		if err != nil {
			// Check whether the connection terminated cleanly in-between packets.
			if err == io.EOF {
				err = nil
			}
			// we don't care about releasing allocated pages here, the server will quit and the allocator freed
			break
		}

		pkt, err = makePacket(rxPacket{fxp(pktType), pktBytes})
		if err != nil {
			switch {
			case errors.Is(err, errUnknownExtendedPacket):
				//if err := svr.serverConn.sendError(pkt, ErrSshFxOpUnsupported); err != nil {
				//	debug("failed to send err packet: %v", err)
				//	svr.conn.Close() // shuts down recvPacket
				//	break
				//}
			default:
				debug("makePacket err: %v", err)
				svr.conn.Close() // shuts down recvPacket
				break
			}
		}

		pktChan <- svr.pktMgr.newOrderedRequest(pkt)
	}

	close(pktChan) // shuts down sftpServerWorkers
	wg.Wait()      // wait for all workers to exit

	// close any still-open files
	for handle, file := range svr.openFiles {
		fmt.Fprintf(svr.debugStream, "sftp server file with handle %q left open: %v\n", handle, file.Name())
		file.Close()
	}
	return err // error from recvPacket
}

type id interface {
	id() uint32
}

// The init packet has no ID, so we just return a zero-value ID
func (p *sshFxInitPacket) id() uint32 { return 0 }

type sshFxpStatResponse struct {
	ID   uint32
	info os.FileInfo
}

func (p *sshFxpStatResponse) marshalPacket() ([]byte, []byte, error) {
	l := 4 + 1 + 4 // uint32(length) + byte(type) + uint32(id)

	b := make([]byte, 4, l)
	b = append(b, sshFxpAttrs)
	b = marshalUint32(b, p.ID)

	var payload []byte
	payload = marshalFileInfo(payload, p.info)

	return b, payload, nil
}

func (p *sshFxpStatResponse) MarshalBinary() ([]byte, error) {
	header, payload, err := p.marshalPacket()
	return append(header, payload...), err
}

var emptyFileStat = []interface{}{uint32(0)}

func (p *sshFxpOpenPacket) readonly() bool {
	return !p.hasPflags(sshFxfWrite)
}

func (p *sshFxpOpenPacket) hasPflags(flags ...uint32) bool {
	for _, f := range flags {
		if p.Pflags&f == 0 {
			return false
		}
	}
	return true
}
func (p *sshFxpOpenPacket) respond(svr *Server) responsePacket {
	// var osFlags int
	// if p.hasPflags(sshFxfRead, sshFxfWrite) {
	// 	osFlags |= os.O_RDWR
	// } else if p.hasPflags(sshFxfWrite) {
	// 	osFlags |= os.O_WRONLY
	// } else if p.hasPflags(sshFxfRead) {
	// 	osFlags |= os.O_RDONLY
	// } else {
	// 	// how are they opening?
	// 	return statusFromError(p.ID, syscall.EINVAL)
	// }

	// Don't use O_APPEND flag as it conflicts with WriteAt.
	// The sshFxfAppend flag is a no-op here as the client sends the offsets.

	// if p.hasPflags(sshFxfCreat) {
	// 	osFlags |= os.O_CREATE
	// }
	// if p.hasPflags(sshFxfTrunc) {
	// 	osFlags |= os.O_TRUNC
	// }
	// if p.hasPflags(sshFxfExcl) {
	// 	osFlags |= os.O_EXCL
	// }

	// mode := os.FileMode(0o644)
	// Like OpenSSH, we only handle permissions here, and only when the file is being created.
	// Otherwise, the permissions are ignored.
	// if p.Flags&sshFileXferAttrPermissions != 0 {
	// 	fs, err := p.unmarshalFileStat(p.Flags)
	// 	if err != nil {
	// 		return statusFromError(p.ID, err)
	// 	}
	// 	mode = fs.FileMode() & os.ModePerm
	// }
	fmt.Fprintf(svr.debugStream, "Path in packet: %v\n", p.Path)
	path := svr.toLocalPath(p.Path)
	fmt.Fprintln(svr.debugStream, path)

	host := "10.45.129.247"
	// target := "/default-container-17246812063770"
	target := "/" + strings.Split(p.Path, "/")[1]
	modifiedPath := strings.Split(p.Path, target)[1]
	fmt.Println(target)
	fmt.Println(modifiedPath)

	mount, err := nfs.DialMount(host)
	if err != nil {
		fmt.Fprint(svr.debugStream, err.Error())
	}
	// defer mount.Close()

	hostname, _ := os.Hostname()
	auth := rpc.NewAuthUnix(hostname, 1000, 1000)

	v, err := mount.Mount(target, auth.Auth())
	if err != nil {
		fmt.Fprint(svr.debugStream, err.Error())
	}
	// defer v.Close()

	svr.nfsTarget = v

	fileSize, err := determineFileSize(svr, modifiedPath)
	if err != nil {
		fmt.Fprintln(svr.debugStream, "Error")
		fmt.Fprintln(svr.debugStream, err.Error())
		return statusFromError(p.ID, err)
	}

	fmt.Fprintf(svr.debugStream, "Determined file size is: %v\n", fileSize)

	svr.nfsFileSize = fileSize

	f, err := svr.nfsTarget.OpenFile(modifiedPath, 0666)

	if err != nil {
		fmt.Fprintln(svr.debugStream, "Error")
		fmt.Fprintln(svr.debugStream, err.Error())
	}
	svr.nfsFile = f
	// wg.Add(1)
	// go nfsManager(svr.nfsFile)
	// f, err := os.OpenFile(svr.toLocalPath(p.Path), osFlags, mode)
	// if err != nil {
	// 	return statusFromError(p.ID, err)
	// }

	var dummyFile *os.File = nil
	handle := svr.nextHandle(dummyFile)
	fmt.Fprintf(svr.debugStream, "handle: %v\n", handle)
	return &sshFxpHandlePacket{ID: p.ID, Handle: handle}
}

func determineFileSize(svr *Server, path string) (int64, error) {
	file, err := svr.nfsTarget.OpenFile(path, 0666)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return 0, err
	}
	defer file.Close()

	var fileSize int64 = 0
	buffer := make([]byte, 1024*4) // Create a buffer to hold chunks of the file
	var bytesRead int
	for {
		// Read the file in chunks
		bytesRead, err = file.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break // End of file reached
			}
			fmt.Println("Error reading file:", err)
			return 0, err
		}
		fileSize += int64(bytesRead)
	}
	fileSize += int64(bytesRead)
	return fileSize, nil
}

func (p *sshFxpOpenPacket) respondOld(svr *Server) string {
	// var osFlags int
	// if p.hasPflags(sshFxfRead, sshFxfWrite) {
	// 	osFlags |= os.O_RDWR
	// } else if p.hasPflags(sshFxfWrite) {
	// 	osFlags |= os.O_WRONLY
	// } else if p.hasPflags(sshFxfRead) {
	// 	osFlags |= os.O_RDONLY
	// }
	// } else {
	// 	// how are they opening?
	// 	return statusFromError(p.ID, syscall.EINVAL)
	// }

	// Don't use O_APPEND flag as it conflicts with WriteAt.
	// The sshFxfAppend flag is a no-op here as the client sends the offsets.

	// if p.hasPflags(sshFxfCreat) {
	// 	osFlags |= os.O_CREATE
	// }
	// if p.hasPflags(sshFxfTrunc) {
	// 	osFlags |= os.O_TRUNC
	// }
	// if p.hasPflags(sshFxfExcl) {
	// 	osFlags |= os.O_EXCL
	// }

	// mode := os.FileMode(0o644)
	// Like OpenSSH, we only handle permissions here, and only when the file is being created.
	// Otherwise, the permissions are ignored.
	// if p.Flags&sshFileXferAttrPermissions != 0 {
	// 	fs, err := p.unmarshalFileStat(p.Flags)
	// 	if err != nil {
	// 		return ""
	// 		// return statusFromError(p.ID, err)
	// 	}
	// 	mode = fs.FileMode() & os.ModePerm
	// }
	// fmt.Fprintf(svr.debugStream, "local path file: %v\n", svr.toLocalPath(p.Path))
	// f, err := os.OpenFile("/home/nutanix/sahil2"+svr.toLocalPath(p.Path), osFlags, mode)
	// if err != nil {
	// 	return "this is the issue\n"
	// 	// return statusFromError(p.ID, err)
	// }
	var f *os.File = nil
	// TODO: This needs to be removed and no local file should be created
	return svr.nextHandle(f)
	// return &sshFxpHandlePacket{ID: p.ID, Handle: handle}
}

func (p *sshFxpReaddirPacket) respond(svr *Server) responsePacket {
	f, ok := svr.getHandle(p.Handle)
	if !ok {
		return statusFromError(p.ID, EBADF)
	}

	dirents, err := f.Readdir(128)
	if err != nil {
		return statusFromError(p.ID, err)
	}

	idLookup := osIDLookup{}

	ret := &sshFxpNamePacket{ID: p.ID}
	for _, dirent := range dirents {
		ret.NameAttrs = append(ret.NameAttrs, &sshFxpNameAttr{
			Name:     dirent.Name(),
			LongName: runLs(idLookup, dirent),
			Attrs:    []interface{}{dirent},
		})
	}
	return ret
}

func (p *sshFxpSetstatPacket) respond(svr *Server) responsePacket {
	path := svr.toLocalPath(p.Path)

	debug("setstat name %q", path)

	fs, err := p.unmarshalFileStat(p.Flags)

	if err == nil && (p.Flags&sshFileXferAttrSize) != 0 {
		err = os.Truncate(path, int64(fs.Size))
	}
	if err == nil && (p.Flags&sshFileXferAttrPermissions) != 0 {
		err = os.Chmod(path, fs.FileMode())
	}
	if err == nil && (p.Flags&sshFileXferAttrUIDGID) != 0 {
		err = os.Chown(path, int(fs.UID), int(fs.GID))
	}
	if err == nil && (p.Flags&sshFileXferAttrACmodTime) != 0 {
		err = os.Chtimes(path, fs.AccessTime(), fs.ModTime())
	}

	return statusFromError(p.ID, err)
}

func (p *sshFxpFsetstatPacket) respond(svr *Server) responsePacket {
	f, ok := svr.getHandle(p.Handle)
	if !ok {
		return statusFromError(p.ID, EBADF)
	}

	path := f.Name()

	debug("fsetstat name %q", path)

	fs, err := p.unmarshalFileStat(p.Flags)

	if err == nil && (p.Flags&sshFileXferAttrSize) != 0 {
		err = f.Truncate(int64(fs.Size))
	}
	if err == nil && (p.Flags&sshFileXferAttrPermissions) != 0 {
		err = f.Chmod(fs.FileMode())
	}
	if err == nil && (p.Flags&sshFileXferAttrUIDGID) != 0 {
		err = f.Chown(int(fs.UID), int(fs.GID))
	}
	if err == nil && (p.Flags&sshFileXferAttrACmodTime) != 0 {
		type chtimer interface {
			Chtimes(atime, mtime time.Time) error
		}

		switch f := interface{}(f).(type) {
		case chtimer:
			// future-compatible, for when/if *os.File supports Chtimes.
			err = f.Chtimes(fs.AccessTime(), fs.ModTime())
		default:
			err = os.Chtimes(path, fs.AccessTime(), fs.ModTime())
		}
	}

	return statusFromError(p.ID, err)
}

func statusFromError(id uint32, err error) *sshFxpStatusPacket {
	ret := &sshFxpStatusPacket{
		ID: id,
		StatusError: StatusError{
			// sshFXOk               = 0
			// sshFXEOF              = 1
			// sshFXNoSuchFile       = 2 ENOENT
			// sshFXPermissionDenied = 3
			// sshFXFailure          = 4
			// sshFXBadMessage       = 5
			// sshFXNoConnection     = 6
			// sshFXConnectionLost   = 7
			// sshFXOPUnsupported    = 8
			Code: sshFxOk,
		},
	}
	if err == nil {
		return ret
	}

	debug("statusFromError: error is %T %#v", err, err)
	ret.StatusError.Code = sshFxFailure
	ret.StatusError.msg = err.Error()

	if os.IsNotExist(err) {
		ret.StatusError.Code = sshFxNoSuchFile
		return ret
	}
	if code, ok := translateSyscallError(err); ok {
		ret.StatusError.Code = code
		return ret
	}

	if errors.Is(err, io.EOF) {
		ret.StatusError.Code = sshFxEOF
		return ret
	}

	var e fxerr
	if errors.As(err, &e) {
		ret.StatusError.Code = uint32(e)
		return ret
	}

	return ret
}
