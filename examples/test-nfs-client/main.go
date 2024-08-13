package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/vmware/go-nfs-client/nfs"
	"github.com/vmware/go-nfs-client/nfs/rpc"
)

func main() {
	host := "10.96.96.51"
	target := "/default-container-60322224114917"

	mount, err := nfs.DialMount(host)
	if err != nil {
		fmt.Print(err.Error())
	}
	// defer mount.Close()

	hostname, _ := os.Hostname()
	auth := rpc.NewAuthUnix(hostname, 1000, 1000)

	v, err := mount.Mount(target, auth.Auth())
	if err != nil {
		fmt.Printf("%v\n", err.Error())
	}

	f, _ := v.OpenFile("test.vmdk", 0666)
	var osFlags int
	osFlags |= os.O_RDWR
	mode := os.FileMode(0o644)
	lf, _ := os.OpenFile("./myfile", osFlags, mode)
	file, _ := os.Open("./sftp_full_trace.out")
	scanner := bufio.NewScanner(file)

	const maxCapacity int = 65536 // your required line length
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()
		res1 := strings.Split(line, ",")
		i, _ := strconv.Atoi(res1[1])
		j, _ := strconv.Atoi(res1[2])
		if res1[0] == "read" {
			f.Seek(int64(i), io.SeekStart)
			f.Read(make([]byte, j))
		} else {
			lf.Seek(0, io.SeekStart)
			data := make([]byte, j)
			lf.Read(data)
			f.Seek(int64(i), io.SeekStart)
			f.Write(data)
		}
	}
}
