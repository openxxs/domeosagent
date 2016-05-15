package netlink

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"os"
	"syscall"
)

type Connection struct {
	// netlink socket
	fd int
	// cache pid to use in every netlink request.
	pid uint32
	// sequence number for netlink messages.
	seq  uint32
	addr syscall.SockaddrNetlink
	rbuf *bufio.Reader
}

// Create and bind a new netlink socket.
func newConnection() (*Connection, error) {

	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM, syscall.NETLINK_GENERIC)
	if err != nil {
		return nil, err
	}

	conn := new(Connection)
	conn.fd = fd
	conn.seq = 0
	conn.pid = uint32(os.Getpid())
	conn.addr.Family = syscall.AF_NETLINK
	conn.rbuf = bufio.NewReader(conn)
	err = syscall.Bind(fd, &conn.addr)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return conn, err
}

func (self *Connection) Read(b []byte) (n int, err error) {
	n, _, err = syscall.Recvfrom(self.fd, b, 0)
	return n, err
}

func (self *Connection) Write(b []byte) (n int, err error) {
	err = syscall.Sendto(self.fd, b, 0, &self.addr)
	return len(b), err
}

func (self *Connection) Close() error {
	return syscall.Close(self.fd)
}

func (self *Connection) WriteMessage(msg syscall.NetlinkMessage) error {
	w := bytes.NewBuffer(nil)
	msg.Header.Len = uint32(syscall.NLMSG_HDRLEN + len(msg.Data))
	msg.Header.Seq = self.seq
	self.seq++
	msg.Header.Pid = self.pid
	binary.Write(w, binary.LittleEndian, msg.Header)
	_, err := w.Write(msg.Data)
	if err != nil {
		return err
	}
	_, err = self.Write(w.Bytes())
	return err
}

func (self *Connection) ReadMessage() (msg syscall.NetlinkMessage, err error) {
	err = binary.Read(self.rbuf, binary.LittleEndian, &msg.Header)
	if err != nil {
		return msg, err
	}
	msg.Data = make([]byte, msg.Header.Len-syscall.NLMSG_HDRLEN)
	_, err = self.rbuf.Read(msg.Data)
	return msg, err
}