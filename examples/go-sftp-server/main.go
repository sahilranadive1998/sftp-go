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
	var activeConns = 0

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

	// Run the timer in the background as a goroutine
	go startBackgroundTimer(&activeConns)

	// Accept multiple incoming connections
	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Printf("failed to accept incoming connection: %v", err)
			continue
		}

		// Handle each connection in a separate goroutine
		go handleConnection(nConn, config, readOnly, debugStream, &activeConns)
	}
	logFile.Close()
}

// handleConnection handles a single SSH/SFTP connection.
func handleConnection(nConn net.Conn, config *ssh.ServerConfig, readOnly bool, debugStream io.Writer, activeConns *int) {
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
		*activeConns += 1
		// Serve the SFTP requests
		if err := server.Serve(); err != nil {
			if err != io.EOF {
				log.Printf("SFTP server completed with error: %v", err)
			}
		}
		server.Close()
		*activeConns -= 1
		log.Printf("SFTP client exited session. Conns: %v", *activeConns)
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

// Function to monitor the variable in the background
func startBackgroundTimer(monitoredVar *int) {
	// Create a timer for 15 minutes
	exitTimer := time.NewTimer(2 * time.Minute)
	defer exitTimer.Stop()

	// Create a ticker that checks the variable every minute
	checkInterval := time.NewTicker(1 * time.Minute)
	defer checkInterval.Stop()

	for {
		select {
		case <-checkInterval.C:
			// Check if the variable is still 0
			if *monitoredVar != 0 {
				// Reset the timer if the variable is not zero
				exitTimer.Reset(2 * time.Minute)
				fmt.Println("Variable is not zero, resetting timer.")
			} else {
				fmt.Println("Variable is still zero, checking again.")
			}

		case <-exitTimer.C:
			// If 15 minutes passed with the variable being 0, exit the program
			fmt.Println("Exiting because variable was 0 for 15 minutes.")
			os.Exit(0)
		}
	}
}
