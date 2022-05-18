package tap

import (
	"time"

	"github.com/google/gopacket/reassembly"
)

type tcpReaderDataMsg struct {
	dir       reassembly.TCPFlowDirection
	stream    *tcpStream
	bytes     []byte
	timestamp time.Time
}

func NewTcpReaderDataMsg(dir reassembly.TCPFlowDirection, stream *tcpStream, data []byte, timestamp time.Time) *tcpReaderDataMsg {
	return &tcpReaderDataMsg{dir, stream, data, timestamp}
}

func (dataMsg *tcpReaderDataMsg) GetBytes() []byte {
	return dataMsg.bytes
}

func (dataMsg *tcpReaderDataMsg) GetTimestamp() time.Time {
	return dataMsg.timestamp
}
