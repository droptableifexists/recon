package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/droptableifexists/recon/sql-proxy/api"
	"github.com/droptableifexists/recon/sql-proxy/store"
)

func main() {
	// Get configuration from environment variables with defaults
	listenPort := getEnv("LISTEN_PORT", "5433")
	backendHost := getEnv("BACKEND_HOST", "postgres")
	backendPort := getEnv("BACKEND_PORT", "5432")

	// The address on which our proxy listens
	listenAddr := ":" + listenPort
	// The actual Postgres server address
	backendAddr := backendHost + ":" + backendPort

	qs := store.MakeQueryStore()
	a := api.MakeQueriesExecutedAPI(qs)
	go a.RunApi()
	// Listen for incoming client connections
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()
	fmt.Printf("Proxy listening on %s, forwarding to %s\n", listenAddr, backendAddr)

	// Handle incoming client connections
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go handleClient(clientConn, backendAddr, qs)
	}
}

func handleClient(clientConn net.Conn, backendAddr string, qs *store.QueryStore) {
	defer clientConn.Close()

	// Connect to the backend (Postgres server)
	backendConn, err := net.Dial("tcp", backendAddr)
	if err != nil {
		log.Printf("Failed to connect to backend: %v", err)
		return
	}
	defer backendConn.Close()

	// Proxy data from client to backend
	go proxyData(clientConn, backendConn, qs)
	// Proxy data from backend to client
	proxyData(backendConn, clientConn, qs)
}

// proxyData forwards data between two connections
func proxyData(src net.Conn, dst net.Conn, qs *store.QueryStore) {
	buffer := make([]byte, 4096)
	for {
		// Read data from source
		n, err := src.Read(buffer)
		if err != nil {
			log.Printf("Error reading from source: %v", err)
			return
		}

		if buffer[0] == 'Q' {
			queryBytes := bytes.Trim(buffer[1:n], "\x00")
			rawDataString := string(queryBytes)
			fmt.Print("\n Query Passing thru: \n")
			fmt.Print(rawDataString[1:])
			qs.AddQuery(store.QueryExecuted{
				Query: rawDataString[1:],
			})
			fmt.Print("\n End")
		}

		// Write data to destination
		_, err = dst.Write(buffer[:n])
		if err != nil {
			log.Printf("Error writing to destination: %v", err)
			return
		}
	}
}

// Helper function to get environment variables with defaults
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
