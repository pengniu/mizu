package tap

import (
	"encoding/hex"
	"os"
	"os/signal"

	// "sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/reassembly"
	"github.com/up9inc/mizu/logger"
	"github.com/up9inc/mizu/tap/api"
	"github.com/up9inc/mizu/tap/dbgctl"
	"github.com/up9inc/mizu/tap/diagnose"
	"github.com/up9inc/mizu/tap/source"
)

const PACKETS_SEEN_LOG_THRESHOLD = 1000

type tcpAssembler struct {
	*reassembly.Assembler
	streamPool    *reassembly.StreamPool
	streamFactory *tcpStreamFactory
	// assemblerMutex sync.Mutex
	ignoredPorts           []uint16
	staleConnectionTimeout time.Duration
	processor              *tcpStreamProcessor
}

// Context
// The assembler context
type context struct {
	CaptureInfo gopacket.CaptureInfo
	Origin      api.Capture
}

func (c *context) GetCaptureInfo() gopacket.CaptureInfo {
	return c.CaptureInfo
}

// func NewTcpAssembler(outputItems chan *api.OutputChannelItem, streamsMap api.TcpStreamMap, opts *TapOpts) *tcpAssembler {
func NewTcpAssembler(outputItems chan *api.OutputChannelItem, opts *TapOpts, processor *tcpStreamProcessor) *tcpAssembler {
	var emitter api.Emitter = &api.Emitting{
		AppStats:      &diagnose.AppStats,
		OutputChannel: outputItems,
	}

	go processor.process()

	// streamFactory := NewTcpStreamFactory(emitter, streamsMap, opts)
	streamFactory := NewTcpStreamFactory(emitter, opts, processor)
	streamPool := reassembly.NewStreamPool(streamFactory)
	assembler := reassembly.NewAssembler(streamPool)

	maxBufferedPagesTotal := GetMaxBufferedPagesPerConnection()
	maxBufferedPagesPerConnection := GetMaxBufferedPagesTotal()
	logger.Log.Infof("Assembler options: maxBufferedPagesTotal=%d, maxBufferedPagesPerConnection=%d, opts=%v",
		maxBufferedPagesTotal, maxBufferedPagesPerConnection, opts)
	assembler.AssemblerOptions.MaxBufferedPagesTotal = 10
	assembler.AssemblerOptions.MaxBufferedPagesPerConnection = 10
	// assembler.AssemblerOptions.MaxBufferedPagesTotal = maxBufferedPagesTotal
	// assembler.AssemblerOptions.MaxBufferedPagesPerConnection = maxBufferedPagesPerConnection

	return &tcpAssembler{
		Assembler:              assembler,
		streamPool:             streamPool,
		streamFactory:          streamFactory,
		ignoredPorts:           opts.IgnoredPorts,
		staleConnectionTimeout: opts.staleConnectionTimeout,
		processor:              processor,
	}
}

func (a *tcpAssembler) processPacket(packetInfo *source.TcpPacketInfo, dumpPacket bool) bool {
	packetsCount := diagnose.AppStats.IncPacketsCount()

	if packetsCount%PACKETS_SEEN_LOG_THRESHOLD == 0 {
		logger.Log.Debugf("Packets seen: #%d", packetsCount)
	}

	packet := packetInfo.Packet
	data := packet.Data()
	diagnose.AppStats.UpdateProcessedBytes(uint64(len(data)))
	if dumpPacket {
		logger.Log.Debugf("Packet content (%d/0x%x) - %s", len(data), len(data), hex.Dump(data))
	}

	tcp := packet.Layer(layers.LayerTypeTCP)
	if tcp != nil {
		diagnose.AppStats.IncTcpPacketsCount()
		tcp := tcp.(*layers.TCP)

		if a.shouldIgnorePort(uint16(tcp.DstPort)) || a.shouldIgnorePort(uint16(tcp.SrcPort)) {
			diagnose.AppStats.IncIgnoredPacketsCount()
		} else {
			// logger.Log.Infof("%v:%v -> %v:%v - (syn: %v) (ack: %v) (fin: %v) (rst: %v) - (seq: %v) (ack: %v) (win: %v)",
			// 	packet.NetworkLayer().NetworkFlow().Src(), packet.TransportLayer().TransportFlow().Src(),
			// 	packet.NetworkLayer().NetworkFlow().Dst(), packet.TransportLayer().TransportFlow().Dst(),
			// 	tcp.SYN, tcp.ACK, tcp.FIN, tcp.RST,
			// 	tcp.Seq, tcp.Ack, tcp.Window)
			net := packet.NetworkLayer().NetworkFlow()
			transport := packet.TransportLayer().TransportFlow()
			connectionId := getConnectionId(net.Src().String(), transport.Src().String(), net.Dst().String(), transport.Dst().String())

			if a.processor.shouldAssemble(connectionId) {
				c := context{
					CaptureInfo: packet.Metadata().CaptureInfo,
					Origin:      packetInfo.Source.Origin,
				}
				diagnose.InternalStats.Totalsz += len(tcp.Payload)
				if !dbgctl.MizuTapperDisableTcpReassembly {
					// a.assemblerMutex.Lock()
					a.AssembleWithContext(packet.NetworkLayer().NetworkFlow(), tcp, &c)
					// a.assemblerMutex.Unlock()
				}
			}
		}
	}

	done := *maxcount > 0 && int64(diagnose.AppStats.PacketsCount) >= *maxcount
	if done {
		errorMapLen, _ := diagnose.TapErrors.GetErrorsSummary()
		logger.Log.Infof("Processed %v packets (%v bytes) in %v (errors: %v, errTypes:%v)",
			diagnose.AppStats.PacketsCount,
			diagnose.AppStats.ProcessedBytes,
			time.Since(diagnose.AppStats.StartTime),
			diagnose.TapErrors.ErrorsCount,
			errorMapLen)
	}

	return done
}

func (a *tcpAssembler) processPackets(dumpPacket bool, packets <-chan source.TcpPacketInfo) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	ticker := time.NewTicker(100 * time.Millisecond)

out:
	for {
		select {
		case packetInfo := <-packets:
			if a.processPacket(&packetInfo, dumpPacket) {
				break out
			}
		case <-signalChan:
			logger.Log.Infof("Caught SIGINT: aborting")
			break out
		case <-ticker.C:
			// a.FlushCloseOlderThan(time.Now().Add(-a.staleConnectionTimeout))
			a.FlushCloseOlderThan(time.Now().Add(-(time.Second * 1)))
		}
	}

	// a.assemblerMutex.Lock()
	closed := a.FlushAll()
	// a.assemblerMutex.Unlock()
	logger.Log.Debugf("Final flush: %d closed", closed)
}

func (a *tcpAssembler) dumpStreamPool() {
	a.streamPool.Dump()
}

func (a *tcpAssembler) waitAndDump() {
	// a.streamFactory.WaitGoRoutines()
	// a.assemblerMutex.Lock()
	logger.Log.Debugf("%s", a.Dump())
	// a.assemblerMutex.Unlock()
}

func (a *tcpAssembler) shouldIgnorePort(port uint16) bool {
	for _, p := range a.ignoredPorts {
		if port == p {
			return true
		}
	}

	return false
}
