package store

import (
	"errors"
	"net"
	"testing"
	"time"
)

// TestPostgresDeadlineConnReadTimesOut 验证无响应连接不会无限阻塞读取。
func TestPostgresDeadlineConnReadTimesOut(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	connection := newPostgresDeadlineConn(client, 20*time.Millisecond)
	startedAt := time.Now()
	_, err := connection.Read(make([]byte, 1))
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("read error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("read timeout elapsed = %s", elapsed)
	}
}

// TestPostgresDeadlineConnWriteTimesOut 验证无响应连接不会无限阻塞写入。
func TestPostgresDeadlineConnWriteTimesOut(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	connection := newPostgresDeadlineConn(client, 20*time.Millisecond)
	startedAt := time.Now()
	_, err := connection.Write([]byte("blocked"))
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("write error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("write timeout elapsed = %s", elapsed)
	}
}

// TestOpenPostgresRejectsInvalidURL 验证数据库工厂在连接前拒绝无效配置。
func TestOpenPostgresRejectsInvalidURL(t *testing.T) {
	database, err := OpenPostgres("://invalid", time.Second)
	if err == nil || database != nil {
		t.Fatalf("database=%v error=%v", database, err)
	}
}
