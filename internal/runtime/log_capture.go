package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// The detached collector owns only inherited descriptors. It survives the CLI
// and exits at pipe EOF, after the service and its children close their output.
func init() {
	if len(os.Args) != 2 || os.Args[1] != "__conven-log-capture" || os.Getenv("_CONVEN_LOG_CAPTURE") != "1" { return }
	input := os.NewFile(3, "service-output")
	output := os.NewFile(4, "service-log")
	index := os.NewFile(5, "log-times")
	if input == nil || output == nil || index == nil { os.Exit(1) }
	err := captureLog(input, output, index)
	input.Close(); output.Close(); index.Close()
	if err != nil { os.Exit(1) }
	os.Exit(0)
}

const logTimeRecordSize = 24

func logTimeHeader(info os.FileInfo) [32]byte {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok { return [32]byte{} }
	return sha256.Sum256([]byte(fmt.Sprintf("conven-log-time-v1:%d:%d", stat.Dev, stat.Ino)))
}

func openLogTimeIndex(log *os.File, path string) (_ *os.File, resultErr error) {
	index, err := openLogWithFlags(path+".timestamps", os.O_CREATE|os.O_RDWR)
	if err != nil { return nil, err }
	defer func() { if resultErr != nil { index.Close() } }()
	info, err := log.Stat()
	if err != nil { return nil, err }
	header := logTimeHeader(info)
	stored := make([]byte, len(header))
	_, readErr := index.ReadAt(stored, 0)
	if readErr != nil || !bytes.Equal(stored, header[:]) {
		if err := index.Truncate(0); err != nil { return nil, err }
		if _, err := index.WriteAt(header[:], 0); err != nil { return nil, err }
	}
	indexInfo, err := index.Stat()
	if err != nil { return nil, err }
	end := int64(32) + (indexInfo.Size()-32)/logTimeRecordSize*logTimeRecordSize
	// Discard partial records and offsets invalidated by a truncated raw log.
	var record [logTimeRecordSize]byte
	for end > 32 {
		if _, err := index.ReadAt(record[:], end-logTimeRecordSize); err != nil { return nil, err }
		if int64(binary.BigEndian.Uint64(record[8:16])) <= info.Size() { break }
		end -= logTimeRecordSize
	}
	if err := index.Truncate(end); err != nil { return nil, err }
	if _, err := index.Seek(end, io.SeekStart); err != nil { return nil, err }
	return index, nil
}

func startLogCapture(log *os.File, path string) (*os.File, error) {
	writer, _, err := startLogCaptureTracked(log, path)
	return writer, err
}

func startLogCaptureTracked(log *os.File, path string) (*os.File, <-chan struct{}, error) {
	index, err := openLogTimeIndex(log, path)
	if err != nil { return nil, nil, err }
	defer index.Close()
	reader, writer, err := os.Pipe()
	if err != nil { return nil, nil, err }
	defer reader.Close()
	executable, err := os.Executable()
	if err != nil { writer.Close(); return nil, nil, err }
	command := exec.Command(executable, "__conven-log-capture")
	command.Env = CommandEnvironment(map[string]string{"_CONVEN_LOG_CAPTURE": "1"})
	command.ExtraFiles = []*os.File{reader, log, index}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil { writer.Close(); return nil, nil, err }
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	return writer, done, nil
}

type collectedOutputWriter struct { output, index *os.File }

func (writer collectedOutputWriter) Write(data []byte) (int, error) {
	if err := captureLog(bytes.NewReader(data), writer.output, writer.index); err != nil { return 0, err }
	return len(data), nil
}

func captureLog(input io.Reader, output, index *os.File) error {
	buffer := make([]byte, 32*1024)
	var record [logTimeRecordSize]byte
	for {
		n, readErr := input.Read(buffer)
		observed := time.Now().UTC().UnixNano()
		if n > 0 {
			if err := syscall.Flock(int(output.Fd()), syscall.LOCK_EX); err != nil { return err }
			written, err := output.Write(buffer[:n])
			if err != nil { syscall.Flock(int(output.Fd()), syscall.LOCK_UN); return err }
			if written != n { syscall.Flock(int(output.Fd()), syscall.LOCK_UN); return io.ErrShortWrite }
			end, err := output.Seek(0, io.SeekCurrent)
			if err != nil { syscall.Flock(int(output.Fd()), syscall.LOCK_UN); return err }
			binary.BigEndian.PutUint64(record[0:8], uint64(end-int64(n)))
			binary.BigEndian.PutUint64(record[8:16], uint64(end))
			binary.BigEndian.PutUint64(record[16:24], uint64(observed))
			// A metadata failure must not interrupt the application's raw logs.
			if index != nil {
				if _, err := index.Write(record[:]); err != nil { index = nil }
			}
			syscall.Flock(int(output.Fd()), syscall.LOCK_UN)
		}
		if readErr == io.EOF { return nil }
		if readErr != nil { return readErr }
	}
}

// Lookup is bounded and keyed by the raw log's inode and byte position. It
// never substitutes file mtime, service start time or the dashboard read time.
func collectedLogTime(index *os.File, offset int64) *time.Time {
	if index == nil { return nil }
	info, err := index.Stat()
	if err != nil || info.Size() < 32 { return nil }
	low, high := int64(0), (info.Size()-32)/logTimeRecordSize
	var record [logTimeRecordSize]byte
	for low < high {
		mid := low + (high-low)/2
		if _, err := index.ReadAt(record[:], 32+mid*logTimeRecordSize); err != nil { return nil }
		start, end := int64(binary.BigEndian.Uint64(record[:8])), int64(binary.BigEndian.Uint64(record[8:16]))
		if offset < start { high = mid } else if offset >= end { low = mid+1 } else {
			value := time.Unix(0, int64(binary.BigEndian.Uint64(record[16:]))).UTC()
			return &value
		}
	}
	return nil
}

func openLogTimes(root, path string, logInfo os.FileInfo) *os.File {
	file, _, _, err := openWebLogFile(root, path+".timestamps")
	if err != nil { return nil }
	header := logTimeHeader(logInfo)
	stored := make([]byte, len(header))
	if _, err := file.ReadAt(stored, 0); err != nil || !bytes.Equal(stored, header[:]) { file.Close(); return nil }
	return file
}
