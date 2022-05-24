package tap

import (
	"os"
	"sync"
	"time"

	"github.com/google/gopacket/reassembly"
	"github.com/struCoder/pidusage"
	"github.com/up9inc/mizu/logger"
	"github.com/up9inc/mizu/tap/diagnose"
)

type tcpStreamProcessor struct {
	streams        map[string]*tcpStream
	newStreams     chan *tcpStream
	removedStreams chan *tcpStream
	payloads       chan *tcpReaderDataMsg
	streamsMapLock sync.RWMutex
	sysInfo        *pidusage.SysInfo
	tapperPid      int
}

func newTcpStreamProcessor() *tcpStreamProcessor {
	return &tcpStreamProcessor{
		streams:        make(map[string]*tcpStream),
		newStreams:     make(chan *tcpStream),
		removedStreams: make(chan *tcpStream),
		payloads:       make(chan *tcpReaderDataMsg),
		tapperPid:      os.Getpid(),
		sysInfo:        &pidusage.SysInfo{Memory: -1, CPU: -1},
	}
}
func (p *tcpStreamProcessor) updateUsage() {
	sysInfo, err := pidusage.GetStat(p.tapperPid)

	if err != nil {
		logger.Log.Warningf("Unable to get CPU Usage for %d", p.tapperPid)
		p.sysInfo = &pidusage.SysInfo{
			CPU:    -1,
			Memory: -1,
		}
		return
	}

	p.sysInfo = sysInfo
}

func (p *tcpStreamProcessor) process() {
	logger.Log.Infof("Running tcp stream processor")
	ticker := time.NewTicker(1000 * time.Millisecond)

	for {
		select {
		case <-ticker.C:
			p.updateUsage()
		case stream := <-p.newStreams:
			p.newStream(stream)
		case stream := <-p.removedStreams:
			p.streamRemoved(stream)
		case payload := <-p.payloads:
			p.newPayload(payload)
		}
	}
}

func (p *tcpStreamProcessor) streamExists(connectionId string) bool {
	p.streamsMapLock.RLock()
	defer p.streamsMapLock.RUnlock()
	_, ok := p.streams[connectionId]
	return ok
}

func (p *tcpStreamProcessor) shouldAssemble(connectionId string) bool {
	if p.streamExists(connectionId) {
		return true
	}

	return p.sysInfo.CPU < 60
}

func (p *tcpStreamProcessor) newStream(stream *tcpStream) {
	if !p.shouldAssemble(stream.connectionId) {
		return
	}

	// logger.Log.Infof("Stream added %s", stream.connectionId)
	diagnose.AppStats.IncLiveTcpStreams()
	p.streamsMapLock.Lock()
	defer p.streamsMapLock.Unlock()
	p.streams[stream.connectionId] = stream
}

func (p *tcpStreamProcessor) streamRemoved(stream *tcpStream) {
	// logger.Log.Infof("Stream removed %s", stream.connectionId)
	p.dissect(stream, stream.client, stream.clientPayloads)
	p.dissect(stream, stream.server, stream.serverPayloads)
	stream.close()

	diagnose.AppStats.DecLiveTcpStreams()
	p.streamsMapLock.Lock()
	defer p.streamsMapLock.Unlock()
	delete(p.streams, stream.connectionId)
}

func (p *tcpStreamProcessor) newPayload(payload *tcpReaderDataMsg) {
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
