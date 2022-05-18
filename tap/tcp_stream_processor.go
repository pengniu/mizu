package tap

import (
	"sync"

	"github.com/google/gopacket/reassembly"
	"github.com/up9inc/mizu/logger"
	"github.com/up9inc/mizu/tap/diagnose"
)

type tcpStreamProcessor struct {
	streams        map[string]*tcpStream
	newStreams     chan *tcpStream
	removedStreams chan *tcpStream
	payloads       chan *tcpReaderDataMsg
}

func newTcpStreamProcessor() *tcpStreamProcessor {
	return &tcpStreamProcessor{
		streams:        make(map[string]*tcpStream),
		newStreams:     make(chan *tcpStream),
		removedStreams: make(chan *tcpStream),
		payloads:       make(chan *tcpReaderDataMsg),
	}
}

func (p *tcpStreamProcessor) process() {
	logger.Log.Infof("Running tcp stream processor")

	for {
		select {
		case stream := <-p.newStreams:
			p.newStream(stream)
		case stream := <-p.removedStreams:
			p.streamRemoved(stream)
		case payload := <-p.payloads:
			p.newPayload(payload)
		}
	}
}
func (p *tcpStreamProcessor) streamExists(stream *tcpStream) bool {
	_, ok := p.streams[stream.connectionId]
	return ok
}

func (p *tcpStreamProcessor) newStream(stream *tcpStream) {
	if !p.streamExists(stream) && len(p.streams) > 1000 {
		return
	}

	// logger.Log.Infof("Stream added %s", stream.connectionId)
	p.streams[stream.connectionId] = stream
	diagnose.AppStats.IncLiveTcpStreams()
}

func (p *tcpStreamProcessor) streamRemoved(stream *tcpStream) {
	if !p.streamExists(stream) {
		stream.close()
		return
	}
	// logger.Log.Infof("Stream removed %s", stream.connectionId)
	p.dissect(stream, stream.client, stream.clientPayloads)
	p.dissect(stream, stream.server, stream.serverPayloads)
	stream.close()
	delete(p.streams, stream.connectionId)
	diagnose.AppStats.DecLiveTcpStreams()
}

func (p *tcpStreamProcessor) newPayload(payload *tcpReaderDataMsg) {
	if !p.streamExists(payload.stream) {
		return
	}
	// logger.Log.Infof("Stream new payload %s", payload.stream.connectionId)
	if payload.dir == reassembly.TCPDirClientToServer {
		payload.stream.clientPayloads = append(payload.stream.clientPayloads, payload)
	} else {
		payload.stream.serverPayloads = append(payload.stream.serverPayloads, payload)
	}
}

func (p *tcpStreamProcessor) dissect(stream *tcpStream, reader *tcpReader, payloads []*tcpReaderDataMsg) {
	// logger.Log.Infof("Dissecting stream %s", stream.connectionId)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		for _, payload := range payloads {
			reader.sendMsgIfNotClosed(payload)
		}
		reader.close()
		wg.Done()
	}()

	reader.run(filteringOptions)
	wg.Wait()
	// logger.Log.Infof("Dissection done %s", stream.connectionId)
}
