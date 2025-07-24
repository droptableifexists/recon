package main

import (
	"bytes"
	"context"
	"encoding/binary"
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
	mu                 sync.RWMutex // Per-connection mutex for better concurrency
}

var connectionStates = make(map[net.Conn]*ConnectionState)
var connectionMutex sync.RWMutex

func getConnectionState(conn net.Conn) *ConnectionState {
	connectionMutex.RLock()
	state, exists := connectionStates[conn]
	connectionMutex.RUnlock()

	if exists {
		state.mu.Lock()
		state.LastActivity = time.Now()
		state.mu.Unlock()
		return state
	}

	// Only acquire write lock when creating new state
	connectionMutex.Lock()
	defer connectionMutex.Unlock()

	// Double-check after acquiring write lock
	if state, exists := connectionStates[conn]; exists {
		state.mu.Lock()
		state.LastActivity = time.Now()
		state.mu.Unlock()
		return state
	}

	state = &ConnectionState{
		PreparedStatements: make(map[string]*PreparedStatement),
		LastActivity:       time.Now(),
	}
	connectionStates[conn] = state
	return state
}

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

// parseBindMessage extracts parameters from a Bind message
// Based on PostgreSQL protocol specification: https://www.postgresql.org/docs/current/protocol-message-formats.html
func parseBindMessage(data []byte) (portalName, statementName string, params []interface{}, err error) {
	// PostgreSQL Bind message format (from official spec):
	// Int32 - Length of message contents in bytes, including self
	// String - Portal name (can be empty)
	// String - Prepared statement name (can be empty)
	// Int16 - Number of parameter format codes that follow (denoted C below)
	// Int16[C] - The parameter format codes. Each must presently be zero (text) or one (binary)
	// Int16 - Number of parameters that follow (denoted N below)
	// Int32[N] - The length of the parameter value, in bytes (this count does not include itself). Can be zero. As a special case, -1 indicates a NULL parameter value. No value bytes follow in the NULL case.
	// Byte[N] - The value of the parameter, in the format indicated by the associated format code. n is the above length.
	// Int16 - Number of result-column format codes that follow (denoted R below)
	// Int16[R] - The result-column format codes. Each must presently be zero (text) or one (binary)

	pos := 0

	// Read portal name (null-terminated string)
	portalName, pos = readNullTerminatedString(data, pos)

	// Read statement name (null-terminated string)
	statementName, pos = readNullTerminatedString(data, pos)

	// Read number of parameter format codes
	if pos+2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter format count")
	}
	numFormats := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2

	// Read parameter format codes (each is 2 bytes)
	formatCodes := make([]int16, numFormats)
	if pos+numFormats*2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter formats")
	}
	for i := 0; i < numFormats; i++ {
		formatCodes[i] = int16(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
	}

	// Read number of parameters
	if pos+2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter count")
	}
	numParams := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2

	// Read parameter lengths and values
	for i := 0; i < numParams; i++ {
		if pos+4 > len(data) {
			return portalName, statementName, nil, fmt.Errorf("message too short for parameter %d length", i+1)
		}

		// Read parameter length (signed int32)
		paramLength := int(int32(binary.BigEndian.Uint32(data[pos : pos+4])))
		pos += 4

		if paramLength == -1 {
			// NULL parameter
			params = append(params, nil)
		} else if paramLength >= 0 {
			if pos+paramLength > len(data) {
				return portalName, statementName, nil, fmt.Errorf("message too short for parameter %d value", i+1)
			}

			// Read parameter value
			paramValue := data[pos : pos+paramLength]

			// Determine format code for this parameter
			formatCode := int16(0) // Default to text format
			if i < len(formatCodes) {
				formatCode = formatCodes[i]
			} else if len(formatCodes) == 1 {
				// If only one format code is provided, it applies to all parameters
				formatCode = formatCodes[0]
			}

			// Decode parameter based on format code
			var decodedParam interface{}
			if formatCode == 0 {
				// Text format - treat as string
				decodedParam = string(paramValue)
			} else if formatCode == 1 {
				// Binary format - try to decode as appropriate type
				decodedParam = decodeBinaryParameter(paramValue)
			} else {
				// Unknown format - treat as string
				decodedParam = string(paramValue)
			}

			params = append(params, decodedParam)
			pos += paramLength
		} else {
			return portalName, statementName, nil, fmt.Errorf("invalid parameter %d length: %d", i+1, paramLength)
		}
	}

	return portalName, statementName, params, nil
}

// decodeBinaryParameter attempts to decode a binary parameter value
func decodeBinaryParameter(data []byte) interface{} {
	if len(data) == 0 {
		return ""
	}

	// Try to decode as different types based on length and content
	switch len(data) {
	case 4:
		// Could be int32, float32, or other 4-byte types
		// Try as int32 first (most common)
		value := int32(binary.BigEndian.Uint32(data))
		return value
	case 8:
		// Could be int64, float64, or other 8-byte types
		// Try as int64 first
		value := int64(binary.BigEndian.Uint64(data))
		return value
	default:
		// For other lengths, check if it's a PostgreSQL array or numeric
		if len(data) >= 4 {
			// Check if it looks like a PostgreSQL array (starts with array header)
			if data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
				return decodePostgreSQLArray(data)
			}

			// Check if it looks like a PostgreSQL numeric (has specific header)
			if len(data) >= 8 && (data[0] == 0 || data[0] == 0x80) {
				return decodePostgreSQLNumeric(data)
			}
		}

		// Check if it looks like text
		isText := true
		for _, b := range data {
			if b < 32 && b != 9 && b != 10 && b != 13 { // Not printable ASCII
				isText = false
				break
			}
		}

		if isText {
			return string(data)
		} else {
			// Return as hex string for binary data
			return fmt.Sprintf("\\x%x", data)
		}
	}
}

// decodePostgreSQLArray attempts to decode a PostgreSQL array
func decodePostgreSQLArray(data []byte) interface{} {
	if len(data) < 20 {
		return fmt.Sprintf("ARRAY[%d bytes]", len(data))
	}

	// PostgreSQL array format:
	// 4 bytes: number of dimensions
	// 4 bytes: flags (has nulls, etc.)
	// 4 bytes: element type OID
	// For each dimension:
	//   4 bytes: size
	//   4 bytes: lower bound

	// Skip header (12 bytes)
	pos := 12

	// Read dimension info
	if pos+8 > len(data) {
		return fmt.Sprintf("ARRAY[%d bytes]", len(data))
	}

	// For 1-dimensional arrays, we have:
	// 4 bytes: size of the dimension
	// 4 bytes: lower bound (usually 1)
	size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 8

	// Now we should have the actual array elements
	// Each element has a 4-byte length followed by the data
	var elements []string
	for i := 0; i < size && pos < len(data); i++ {
		if pos+4 > len(data) {
			break
		}

		// Read element length
		elemLen := int(int32(binary.BigEndian.Uint32(data[pos : pos+4])))
		pos += 4

		if elemLen == -1 {
			// NULL element
			elements = append(elements, "NULL")
		} else if elemLen >= 0 && pos+elemLen <= len(data) {
			// Read element data
			elemData := data[pos : pos+elemLen]
			pos += elemLen

			// Try to decode as text
			elemStr := string(elemData)
			// Filter out control characters and metadata
			if len(elemStr) > 0 && len(elemStr) < 100 && !containsControlChars(elemStr) {
				elements = append(elements, elemStr)
			}
		}
	}

	if len(elements) > 0 {
		return elements
	}

	return fmt.Sprintf("ARRAY[%d elements]", size)
}

// containsControlChars checks if a string contains control characters
func containsControlChars(s string) bool {
	for _, r := range s {
		if r < 32 && r != 9 && r != 10 && r != 13 { // Control chars except tab, newline, carriage return
			return true
		}
	}
	return false
}

// decodePostgreSQLNumeric attempts to decode a PostgreSQL numeric
func decodePostgreSQLNumeric(data []byte) interface{} {
	if len(data) < 8 {
		return fmt.Sprintf("NUMERIC[%d bytes]", len(data))
	}

	// PostgreSQL numeric format:
	// 2 bytes: number of digits before decimal point
	// 2 bytes: number of digits after decimal point
	// 2 bytes: weight (scale factor)
	// 2 bytes: sign (0x0000 = positive, 0x4000 = negative, 0xC000 = NaN)
	// Then the actual digits as 16-bit values

	if len(data) < 8 {
		return fmt.Sprintf("NUMERIC[%d bytes]", len(data))
	}

	// Read header
	ndigits := int(binary.BigEndian.Uint16(data[0:2]))
	weight := int16(binary.BigEndian.Uint16(data[2:4]))
	sign := binary.BigEndian.Uint16(data[4:6])

	// Check for special values
	if sign == 0xC000 {
		return "NaN"
	}

	// For now, return a simplified representation
	// Full numeric decoding would require parsing the digit array
	if sign == 0x4000 {
		return fmt.Sprintf("NUMERIC(-%d digits, weight %d)", ndigits, weight)
	} else {
		return fmt.Sprintf("NUMERIC(%d digits, weight %d)", ndigits, weight)
	}
}

// readNullTerminatedString reads a null-terminated string from the data
func readNullTerminatedString(data []byte, pos int) (string, int) {
	start := pos
	for pos < len(data) && data[pos] != 0 {
		pos++
	}
	if pos < len(data) {
		pos++ // Skip the null terminator
	}
	return string(data[start : pos-1]), pos
}

// cleanParameter removes unwanted characters from parameter values
func cleanParameter(param interface{}) interface{} {
	if param == nil {
		return nil
	}

	paramStr, ok := param.(string)
	if !ok {
		return param
	}

	// Remove protocol artifacts that shouldn't be in parameter values
	cleaned := strings.ReplaceAll(paramStr, "\u0001", "") // Remove SOH (Start of Heading)
	cleaned = strings.ReplaceAll(cleaned, "\u0002", "")   // Remove STX (Start of Text)
	cleaned = strings.ReplaceAll(cleaned, "\u0003", "")   // Remove ETX (End of Text)
	cleaned = strings.ReplaceAll(cleaned, "\u0019", "")   // Remove ETB (End of Transmission Block)

	// Convert Unicode escape sequences to readable characters
	cleaned = strings.ReplaceAll(cleaned, "\\u003e", ">")  // Greater than
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

	// Only clean up formatting artifacts, preserve actual data
	cleaned = strings.TrimSpace(cleaned)

	// Replace formatting whitespace but preserve null bytes and other data
	cleaned = strings.ReplaceAll(cleaned, "\t", " ") // Replace tabs with spaces
	cleaned = strings.ReplaceAll(cleaned, "\n", " ") // Replace newlines with spaces
	cleaned = strings.ReplaceAll(cleaned, "\r", " ") // Replace carriage returns with spaces

	// Collapse multiple spaces into single spaces (but only if they're formatting spaces)
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}

	return cleaned
}

// formatParameterizedQuery combines a query with its parameters
func formatParameterizedQuery(query string, params []interface{}) string {
	if len(params) == 0 {
		return cleanQueryForDisplay(query)
	}

	// Simple parameter substitution for display
	result := query
	for i, param := range params {
		placeholder := fmt.Sprintf("$%d", i+1)
		cleanedParam := cleanParameter(param)
		paramStr := formatParameterForDisplay(cleanedParam)
		result = strings.ReplaceAll(result, placeholder, paramStr)
	}

	return cleanQueryForDisplay(result)
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

// formatParameterForDisplay formats a parameter for display in SQL
func formatParameterForDisplay(param interface{}) string {
	if param == nil {
		return "NULL"
	}

	switch v := param.(type) {
	case string:
		// Quote strings and escape single quotes
		escaped := strings.ReplaceAll(v, "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	case int32, int64, int:
		// Numbers don't need quotes
		return fmt.Sprintf("%v", v)
	case []string:
		// Format arrays
		if len(v) == 0 {
			return "ARRAY[]"
		}
		quoted := make([]string, len(v))
		for i, elem := range v {
			escaped := strings.ReplaceAll(elem, "'", "''")
			quoted[i] = fmt.Sprintf("'%s'", escaped)
		}
		return fmt.Sprintf("ARRAY[%s]", strings.Join(quoted, ", "))
	default:
		// For other types, use the default formatting
		return fmt.Sprintf("%v", v)
	}
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
	a := api.MakeQueriesExecutedAPI(qs)
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
					statementName, query, err := parseParseMessage(parseContent)
					if err == nil && query != "" {
						// Clean the query for display
						cleanedQuery := cleanQueryForDisplay(query)
						stmt := &PreparedStatement{
							Name:  statementName,
							Query: cleanedQuery,
						}
						connState := getConnectionState(src)
						connState.mu.Lock()
						connState.PreparedStatements[statementName] = stmt
						connState.LastParseMessage = stmt // Store for use with empty statement names
						connState.mu.Unlock()

						// Store the query structure immediately when we receive the Parse message
						qs.AddQuery(store.QueryExecuted{
							Query: cleanedQuery,
						})
					}
				}
			case BindMessage:
				// Extended protocol - Bind message (parameters)
				if n > 5 {
					bindContent := buffer[5:n]
					_, statementName, params, err := parseBindMessage(bindContent)
					if err == nil {

						var stmt *PreparedStatement
						var exists bool

						connState := getConnectionState(src)
						// If statement name is empty, use the last Parse message
						if statementName == "" {
							connState.mu.RLock()
							stmt = connState.LastParseMessage
							exists = stmt != nil
							connState.mu.RUnlock()
						} else {
							connState.mu.RLock()
							stmt, exists = connState.PreparedStatements[statementName]
							connState.mu.RUnlock()
						}

						if exists {
							stmt.Params = params
							// Query structure was already stored in Parse message, no need to store again
						}
					}
				}
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
