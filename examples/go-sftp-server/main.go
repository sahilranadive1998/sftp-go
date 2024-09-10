// An example SFTP server implementation using the golang SSH package.
// Serves the whole filesystem visible to the user, and has a hard-coded username and password,
// so not for real use!
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Based on example server code from golang.org/x/crypto/ssh and server_standalone
func main() {
	var (
		readOnly    bool
		debugStderr bool
	)

	flag.BoolVar(&readOnly, "R", false, "read-only server")
	flag.BoolVar(&debugStderr, "e", false, "debug to stderr")
	flag.Parse()
	var logFile *os.File
	debugStream := io.Discard
	if debugStderr {
		logFile, _ := os.Create("./log/log_" + time.Now().Format(time.RFC3339) + ".out")
		debugStream = logFile
	}

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &ssh.ServerConfig{
		NoClientAuth: true,
		// PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		// 	// Should use constant-time compare (or better, salt+hash) in
		// 	// a production setting.
		// 	fmt.Fprintf(debugStream, "Login: %s\n", c.User())
		// 	if c.User() == "testuser" && string(pass) == "tiger" {
		// 		return nil, nil
		// 	}
		// 	return nil, fmt.Errorf("password rejected for %q", c.User())
		// },
	}

	privateBytes, err := os.ReadFile("/home/nutanix/.ssh/id_rsa")
	if err != nil {
		log.Fatal("Failed to load private key", err)
	}

	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		log.Fatal("Failed to parse private key", err)
	}

	config.AddHostKey(private)

	// Once a ServerConfig has been configured, connections can be
	// accepted.
	listener, err := net.Listen("tcp", "127.0.0.1:3373")
	if err != nil {
		log.Fatal("failed to listen for connection", err)
	}
	fmt.Printf("Listening on %v\n", listener.Addr())

	// Accept multiple incoming connections
	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Printf("failed to accept incoming connection: %v", err)
			continue
		}

		// Handle each connection in a separate goroutine
		go handleConnection(nConn, config, readOnly, debugStream)
	}

	// for {
	// 	nConn, err := listener.Accept()
	// 	if err != nil {
	// 		log.Fatal("failed to accept incoming connection", err)
	// 	}

	// 	// Before use, a handshake must be performed on the incoming
	// 	// net.Conn.
	// 	_, chans, reqs, err := ssh.NewServerConn(nConn, config)
	// 	if err != nil {
	// 		log.Fatal("failed to handshake", err)
	// 	}
	// 	fmt.Fprintf(debugStream, "SSH server established\n")

	// 	// The incoming Request channel must be serviced.
	// 	go ssh.DiscardRequests(reqs)

	// 	// Service the incoming Channel channel.
	// 	for newChannel := range chans {
	// 		// Channels have a type, depending on the application level
	// 		// protocol intended. In the case of an SFTP session, this is "subsystem"
	// 		// with a payload string of "<length=4>sftp"
	// 		fmt.Fprintf(debugStream, "Incoming channel: %s\n", newChannel.ChannelType())
	// 		if newChannel.ChannelType() != "session" {
	// 			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
	// 			fmt.Fprintf(debugStream, "Unknown channel type: %s\n", newChannel.ChannelType())
	// 			continue
	// 		}
	// 		channel, requests, err := newChannel.Accept()
	// 		if err != nil {
	// 			log.Fatal("could not accept channel.", err)
	// 		}
	// 		fmt.Fprintf(debugStream, "Channel accepted\n")

	// 		// Sessions have out-of-band requests such as "shell",
	// 		// "pty-req" and "env".  Here we handle only the
	// 		// "subsystem" request.
	// 		go func(in <-chan *ssh.Request) {
	// 			for req := range in {
	// 				fmt.Fprintf(debugStream, "Request: %v\n", req.Type)
	// 				ok := false
	// 				switch req.Type {
	// 				case "subsystem":
	// 					fmt.Fprintf(debugStream, "Subsystem: %s\n", req.Payload[4:])
	// 					if string(req.Payload[4:]) == "sftp" {
	// 						ok = true
	// 					}
	// 				}
	// 				fmt.Fprintf(debugStream, " - accepted: %v\n", ok)
	// 				req.Reply(ok, nil)
	// 			}
	// 		}(requests)

	// 		serverOptions := []sftp.ServerOption{
	// 			sftp.WithDebug(debugStream),
	// 			sftp.WithMaxTxPacket(64 * 1024),
	// 			sftp.WithAllocator(),
	// 		}

	// 		if readOnly {
	// 			serverOptions = append(serverOptions, sftp.ReadOnly())
	// 			fmt.Fprintf(debugStream, "Read-only server\n")
	// 		} else {
	// 			fmt.Fprintf(debugStream, "Read write server\n")
	// 		}

	// 		server, err := sftp.NewServer(
	// 			channel,
	// 			serverOptions...,
	// 		)
	// 		if err != nil {
	// 			log.Fatal(err)
	// 		}
	// 		if err := server.Serve(); err != nil {
	// 			if err != io.EOF {
	// 				log.Fatal("sftp server completed with error:", err)
	// 			}
	// 		}
	// 		server.Close()
	// 		log.Print("sftp client exited session.")
	// 	}
	// }
	logFile.Close()
}

// handleConnection handles a single SSH/SFTP connection.
func handleConnection(nConn net.Conn, config *ssh.ServerConfig, readOnly bool, debugStream io.Writer) {
	defer nConn.Close()

	// Perform SSH handshake
	_, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		log.Printf("failed to handshake: %v", err)
		return
	}
	fmt.Fprintf(debugStream, "SSH server established\n")

	// Discard global requests
	go ssh.DiscardRequests(reqs)

	// Service the incoming Channel channel.
	for newChannel := range chans {
		// Handle only "session" channels
		fmt.Fprintf(debugStream, "Incoming channel: %s\n", newChannel.ChannelType())
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			fmt.Fprintf(debugStream, "Unknown channel type: %s\n", newChannel.ChannelType())
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("could not accept channel: %v", err)
			return
		}
		fmt.Fprintf(debugStream, "Channel accepted\n")

		// Handle requests such as "subsystem"
		go handleRequests(requests, debugStream)

		// Set up the SFTP server
		serverOptions := []sftp.ServerOption{
			sftp.WithDebug(debugStream),
			sftp.WithMaxTxPacket(64 * 1024),
			sftp.WithAllocator(),
		}

		if readOnly {
			serverOptions = append(serverOptions, sftp.ReadOnly())
			fmt.Fprintf(debugStream, "Read-only server\n")
		} else {
			fmt.Fprintf(debugStream, "Read write server\n")
		}

		server, err := sftp.NewServer(
			channel,
			serverOptions...,
		)
		if err != nil {
			log.Printf("failed to start SFTP server: %v", err)
			return
		}

		// Serve the SFTP requests
		if err := server.Serve(); err != nil {
			if err != io.EOF {
				log.Printf("SFTP server completed with error: %v", err)
			}
		}
		server.Close()
		log.Print("SFTP client exited session.")
	}
}

// handleRequests handles out-of-band requests like "subsystem".
func handleRequests(in <-chan *ssh.Request, debugStream io.Writer) {
	for req := range in {
		fmt.Fprintf(debugStream, "Request: %v\n", req.Type)
		ok := false
		switch req.Type {
		case "subsystem":
			fmt.Fprintf(debugStream, "Subsystem: %s\n", req.Payload[4:])
			if string(req.Payload[4:]) == "sftp" {
				ok = true
			}
		}
		fmt.Fprintf(debugStream, " - accepted: %v\n", ok)
		req.Reply(ok, nil)
	}
}
