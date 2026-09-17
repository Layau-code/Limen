package store

import (
	"context"
	"database/sql"
	"net"
	"time"

	"github.com/lib/pq"
)

const defaultPostgresIOTimeout = 5 * time.Second

// OpenPostgres 创建带有限网络读写期限的 PostgreSQL 连接池。
func OpenPostgres(databaseURL string, ioTimeout time.Duration) (*sql.DB, error) {
	connector, err := pq.NewConnector(databaseURL)
	if err != nil {
		return nil, err
	}
	if ioTimeout <= 0 {
		ioTimeout = defaultPostgresIOTimeout
	}
	connector.Dialer(postgresDeadlineDialer{ioTimeout: ioTimeout})
	return sql.OpenDB(connector), nil
}

type postgresDeadlineDialer struct {
	dialer    net.Dialer
	ioTimeout time.Duration
}

// Dial 创建带数据库 I/O 期限的网络连接。
func (dialer postgresDeadlineDialer) Dial(network, address string) (net.Conn, error) {
	connection, err := dialer.dialer.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return newPostgresDeadlineConn(connection, dialer.ioTimeout), nil
}

// DialTimeout 在连接期限内创建带数据库 I/O 期限的网络连接。
func (dialer postgresDeadlineDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	connection, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}
	return newPostgresDeadlineConn(connection, dialer.ioTimeout), nil
}

// DialContext 使用调用方 Context 创建带数据库 I/O 期限的网络连接。
func (dialer postgresDeadlineDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := dialer.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return newPostgresDeadlineConn(connection, dialer.ioTimeout), nil
}

type postgresDeadlineConn struct {
	net.Conn
	ioTimeout time.Duration
}

// newPostgresDeadlineConn 为每次数据库读写刷新有限期限。
func newPostgresDeadlineConn(connection net.Conn, ioTimeout time.Duration) net.Conn {
	return &postgresDeadlineConn{Conn: connection, ioTimeout: ioTimeout}
}

// Read 在有限期限内读取数据库响应。
func (connection *postgresDeadlineConn) Read(buffer []byte) (int, error) {
	if err := connection.SetReadDeadline(time.Now().Add(connection.ioTimeout)); err != nil {
		return 0, err
	}
	return connection.Conn.Read(buffer)
}

// Write 在有限期限内发送数据库请求。
func (connection *postgresDeadlineConn) Write(buffer []byte) (int, error) {
	if err := connection.SetWriteDeadline(time.Now().Add(connection.ioTimeout)); err != nil {
		return 0, err
	}
	return connection.Conn.Write(buffer)
}
