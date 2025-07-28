package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/droptableifexists/recon/sql-proxy/api"
	"github.com/droptableifexists/recon/sql-proxy/store"
)

// PostgreSQL message types
const (
	QueryMessage    = 'Q'
	ParseMessage    = 'P'
	BindMessage     = 'B'
	ExecuteMessage  = 'E'
	DescribeMessage = 'D'
	CloseMessage    = 'C'
	SyncMessage     = 'S'
)

// Track prepared statements for extended protocol
type PreparedStatement struct {
	Name   string
	Query  string
	Params []interface{}
}

// Track prepared statements per connection to avoid conflicts
type ConnectionState struct {
	PreparedStatements map[string]*PreparedStatement
	LastParseMessage   *PreparedStatement
	LastActivity       time.Time
}

var connectionStates = make(map[net.Conn]*ConnectionState)
var connectionMutex sync.RWMutex

func removeConnectionState(conn net.Conn) {
	connectionMutex.Lock()
	defer connectionMutex.Unlock()
	delete(connectionStates, conn)
}

// cleanupInactiveConnections removes connections that have been inactive for too long
func cleanupInactiveConnections() {
	// Get cleanup configuration from environment
	cleanupInterval := getEnv("CLEANUP_INTERVAL", "30s")
	cleanupTimeout := getEnv("CLEANUP_TIMEOUT", "2m")

	interval, err := time.ParseDuration(cleanupInterval)
	if err != nil {
		interval = 30 * time.Second
		fmt.Printf("Invalid CLEANUP_INTERVAL, using default: 30s\n")
	}

	timeout, err := time.ParseDuration(cleanupTimeout)
	if err != nil {
		timeout = 2 * time.Minute
		fmt.Printf("Invalid CLEANUP_TIMEOUT, using default: 2m\n")
	}

	fmt.Printf("Starting connection cleanup: interval=%v, timeout=%v\n", interval, timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		connectionMutex.Lock()
		now := time.Now()
		var toRemove []net.Conn

		for conn, state := range connectionStates {
			// Remove connections inactive for longer than the configured timeout
			if now.Sub(state.LastActivity) > timeout {
				toRemove = append(toRemove, conn)
			}
		}

		for _, conn := range toRemove {
			delete(connectionStates, conn)
		}

		if len(toRemove) > 0 {
			fmt.Printf("Cleaned up %d inactive connections. Active connections: %d\n",
				len(toRemove), len(connectionStates))
		}
		connectionMutex.Unlock()
	}
}

// logConnectionStats periodically logs connection statistics
func logConnectionStats() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		connectionMutex.RLock()
		activeConnections := len(connectionStates)
		connectionMutex.RUnlock()

		log.Printf("Connection stats: %d active connections", activeConnections)
	}
}

// parseParseMessage extracts the query from a Parse message
func parseParseMessage(data []byte) (statementName, query string, err error) {
	// Parse message format: statement_name\0query\0num_param_types
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid parse message format")
	}

	statementName = string(parts[0])
	query = string(parts[1])
	return statementName, query, nil
}

// cleanQueryForDisplay cleans up the final query for better display
func cleanQueryForDisplay(query string) string {
	// Replace Unicode escape sequences with readable characters
	cleaned := strings.ReplaceAll(query, "\\u003e", ">")   // Greater than
	cleaned = strings.ReplaceAll(cleaned, "\\u003c", "<")  // Less than
	cleaned = strings.ReplaceAll(cleaned, "\\u003d", "=")  // Equals
	cleaned = strings.ReplaceAll(cleaned, "\\u0026", "&")  // Ampersand
	cleaned = strings.ReplaceAll(cleaned, "\\u0027", "'")  // Single quote
	cleaned = strings.ReplaceAll(cleaned, "\\u0022", "\"") // Double quote

	// Also handle the actual Unicode characters (not escaped)
	cleaned = strings.ReplaceAll(cleaned, "\u003e", ">")  // Greater than
	cleaned = strings.ReplaceAll(cleaned, "\u003c", "<")  // Less than
	cleaned = strings.ReplaceAll(cleaned, "\u003d", "=")  // Equals
	cleaned = strings.ReplaceAll(cleaned, "\u0026", "&")  // Ampersand
	cleaned = strings.ReplaceAll(cleaned, "\u0027", "'")  // Single quote
	cleaned = strings.ReplaceAll(cleaned, "\u0022", "\"") // Double quote

	return cleaned
}

func main() {
	// Get configuration from environment variables with defaults
	listenPort := getEnv("LISTEN_PORT", "5433")
	backendHost := getEnv("BACKEND_HOST", "postgres")
	backendPort := getEnv("BACKEND_PORT", "5432")
	passThroughPort := getEnv("PASS_THROUGH_PORT", "5434")

	// The address on which our proxy listens
	listenAddr := ":" + listenPort
	// The actual Postgres server address
	backendAddr := backendHost + ":" + backendPort
	// port to pass through to the original Postgres server
	passThroughAddr := ":" + passThroughPort

	qs := store.MakeQueryStore()
	ss := store.MakeSchemaStore()
	a := api.MakeQueriesExecutedAPI(qs, ss)
	go a.RunApi()

	// Start connection cleanup goroutine
	go cleanupInactiveConnections()

	// Start connection stats logging
	go logConnectionStats()

	// Start the monitored proxy (with query interception)
	go startMonitoredProxy(listenAddr, backendAddr, qs)

	// Start the passthrough proxy (no interception)
	go startPassthroughProxy(passThroughAddr, backendAddr)

	// Keep the main goroutine alive
	select {}
}

func startMonitoredProxy(listenAddr, backendAddr string, qs *store.QueryStore) {
	// Listen for incoming client connections
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("Failed to listen on monitored port: %v", err)
		return
	}
	defer listener.Close()

	fmt.Printf("Monitored proxy listening on %s, forwarding to %s\n",
		listenAddr, backendAddr)

	// Handle incoming client connections
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		go func(conn net.Conn) {
			defer func() {
				conn.Close()
			}()
			handleMonitoredClient(conn, backendAddr, qs)
		}(clientConn)
	}
}

func startPassthroughProxy(listenAddr, backendAddr string) {
	// Listen for incoming client connections
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("Failed to listen on passthrough port: %v", err)
		return
	}
	defer listener.Close()

	fmt.Printf("Passthrough proxy listening on %s, forwarding to %s\n",
		listenAddr, backendAddr)

	// Handle incoming client connections
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		go func(conn net.Conn) {
			defer func() {
				conn.Close()
			}()
			handlePassthroughClient(conn, backendAddr)
		}(clientConn)
	}
}

func handleMonitoredClient(clientConn net.Conn, backendAddr string, qs *store.QueryStore) {
	defer func() {
		clientConn.Close()
		removeConnectionState(clientConn)
	}()

	// Connect to the backend (Postgres server) with faster timeout and less blocking
	var backendConn net.Conn
	var err error

	// Use shorter timeout and fewer retries to reduce blocking
	for attempt := 1; attempt <= 2; attempt++ {
		backendConn, err = net.DialTimeout("tcp", backendAddr, 2*time.Second)
		if err == nil {
			break
		}

		log.Printf("Connection attempt %d failed to backend %s: %v (client: %s)",
			attempt, backendAddr, err, clientConn.RemoteAddr())

		if attempt < 2 {
			// Shorter backoff to reduce blocking
			backoff := time.Duration(attempt) * 50 * time.Millisecond
			time.Sleep(backoff)
		}
	}

	if err != nil {
		log.Printf("All connection attempts failed to backend %s: %v (client: %s)",
			backendAddr, err, clientConn.RemoteAddr())
		return
	}

	// Create a context for coordinating both proxy goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()            // Cancel context to signal both goroutines to stop
		backendConn.Close() // Ensure backend connection is closed
	}()

	log.Printf("Established connection: client %s -> backend %s", clientConn.RemoteAddr(), backendAddr)

	// Use WaitGroup to wait for both proxy goroutines to complete
	var wg sync.WaitGroup
	wg.Add(2)

	// Proxy data from client to backend
	go func() {
		defer wg.Done()
		listenAndProxyDataWithContext(ctx, clientConn, backendConn, qs)
	}()

	// Proxy data from backend to client
	go func() {
		defer wg.Done()
		listenAndProxyDataWithContext(ctx, backendConn, clientConn, qs)
	}()

	// Wait for both goroutines to complete (when either connection closes)
	wg.Wait()
	log.Printf("Connection closed: client %s -> backend %s", clientConn.RemoteAddr(), backendAddr)
}

func handlePassthroughClient(clientConn net.Conn, backendAddr string) {
	defer clientConn.Close()

	// Connect to the backend (Postgres server) with faster timeout
	backendConn, err := net.DialTimeout("tcp", backendAddr, 2*time.Second)
	if err != nil {
		log.Printf("Failed to connect to backend: %v", err)
		return
	}

	// Create a context for coordinating both proxy goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()            // Cancel context to signal both goroutines to stop
		backendConn.Close() // Ensure backend connection is closed
	}()

	// Use WaitGroup to wait for both proxy goroutines to complete
	var wg sync.WaitGroup
	wg.Add(2)

	// Proxy data from client to backend (no processing)
	go func() {
		defer wg.Done()
		dontListenAndProxyDataWithContext(ctx, clientConn, backendConn)
	}()

	// Proxy data from backend to client (no processing)
	go func() {
		defer wg.Done()
		dontListenAndProxyDataWithContext(ctx, backendConn, clientConn)
	}()

	// Wait for both goroutines to complete (when either connection closes)
	wg.Wait()
}

// listenAndProxyDataWithContext forwards data between two connections with context cancellation
func listenAndProxyDataWithContext(ctx context.Context, src net.Conn, dst net.Conn, qs *store.QueryStore) {
	buffer := make([]byte, 4096)

	// Set read deadline to make the connection cancellable
	src.SetReadDeadline(time.Now().Add(1 * time.Second))

	for {
		select {
		case <-ctx.Done():
			// Context was cancelled, exit gracefully
			return
		default:
			// Continue with normal operation
		}

		// Read data from source with timeout
		src.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := src.Read(buffer)
		if err != nil {
			// Check if it's a timeout error (which is expected for context cancellation)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if context was cancelled during the timeout
				select {
				case <-ctx.Done():
					return
				default:
					// Continue reading
					continue
				}
			}

			// Real error - log and exit
			log.Printf("Error reading from source: %v", err)
			return
		}

		// Process the message if it's a PostgreSQL protocol message
		if n > 0 {
			messageType := buffer[0]

			switch messageType {
			case QueryMessage:
				// Simple query protocol
				if n > 5 {
					queryContent := bytes.Trim(buffer[5:n], "\x00")
					query := string(queryContent)
					if query != "" {
						qs.AddQuery(store.QueryExecuted{
							Query: query,
						})
					}
				}
			case ParseMessage:
				// Extended protocol - Parse message
				if n > 5 {
					parseContent := buffer[5:n]
					_, query, err := parseParseMessage(parseContent)
					if err == nil && query != "" {
						// Clean the query for display
						cleanedQuery := cleanQueryForDisplay(query)
						// Store the query structure immediately when we receive the Parse message
						qs.AddQuery(store.QueryExecuted{
							Query: cleanedQuery,
						})
					}
				}
			case BindMessage:
				// Extended protocol - Bind message (parameters)
			case ExecuteMessage:
				// Extended protocol - Execute message
			}
		}

		// Write data to destination
		_, err = dst.Write(buffer[:n])
		if err != nil {
			log.Printf("Error writing to destination: %v", err)
			return
		}
	}
}

// dontListenAndProxyDataWithContext forwards data between two connections with context cancellation (no processing)
func dontListenAndProxyDataWithContext(ctx context.Context, src net.Conn, dst net.Conn) {
	buffer := make([]byte, 4096)

	// Set read deadline to make the connection cancellable
	src.SetReadDeadline(time.Now().Add(1 * time.Second))

	for {
		select {
		case <-ctx.Done():
			// Context was cancelled, exit gracefully
			return
		default:
			// Continue with normal operation
		}

		// Read data from source with timeout
		src.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := src.Read(buffer)
		if err != nil {
			// Check if it's a timeout error (which is expected for context cancellation)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if context was cancelled during the timeout
				select {
				case <-ctx.Done():
					return
				default:
					// Continue reading
					continue
				}
			}

			// Real error - log and exit
			log.Printf("Error reading from source: %v", err)
			return
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
