package track

import (
	"context"
	"time"
	"unsafe"

	. "m7s.live/engine/v4/common"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/util"
)

type 流速控制 struct {
	起始时间戳 uint32
	起始时间  time.Time
	等待上限  time.Duration
}

func (p *流速控制) 重置(绝对时间戳 uint32) {
	p.起始时间 = time.Now()
	p.起始时间戳 = 绝对时间戳
	// println("重置", p.起始时间.Format("2006-01-02 15:04:05"), p.起始时间戳)
}
func (p *流速控制) 时间戳差(绝对时间戳 uint32) time.Duration {
	return time.Duration(绝对时间戳-p.起始时间戳) * time.Millisecond
}
func (p *流速控制) 控制流速(绝对时间戳 uint32) {
	数据时间差, 实际时间差 := p.时间戳差(绝对时间戳), time.Since(p.起始时间)
	// println("数据时间差", 数据时间差, "实际时间差", 实际时间差, "绝对时间戳", 绝对时间戳, "起始时间戳", p.起始时间戳, "起始时间", p.起始时间.Format("2006-01-02 15:04:05"))
	// if 实际时间差 > 数据时间差 {
	// 	p.重置(绝对时间戳)
	// 	return
	// }
	// 如果收到的帧的时间戳超过实际消耗的时间100ms就休息一下，100ms作为一个弹性区间防止频繁调用sleep
	if 过快毫秒 := (数据时间差 - 实际时间差) / time.Millisecond; 过快毫秒 > 100 {
		// println("过快毫秒", 过快毫秒)
		if 过快毫秒 > p.等待上限 {
			time.Sleep(time.Millisecond * p.等待上限)
		} else {
			time.Sleep(过快毫秒 * time.Millisecond)
		}
	} else if 过快毫秒 < -100 {
		// println("过慢毫秒", 过快毫秒)
	}
}

type SpesificTrack[T RawSlice] interface {
	CompleteRTP(*AVFrame[T])
	CompleteAVCC(*AVFrame[T])
	WriteSliceBytes([]byte)
	WriteRTPFrame(*RTPFrame)
	generateTimestamp(uint32)
	Flush()
}

// Media 基础媒体Track类
type Media[T RawSlice] struct {
	Base
	AVRing[T]
	SampleRate           uint32
	SSRC                 uint32
	DecoderConfiguration DecoderConfiguration[T] `json:"-"` //H264(SPS、PPS) H265(VPS、SPS、PPS) AAC(config)
	RTPMuxer
	RTPDemuxer
	SpesificTrack[T] `json:"-"`
	流速控制
}

func (av *Media[T]) SetSpeedLimit(value int) {
	av.等待上限 = time.Duration(value)
}

func (av *Media[T]) SetStuff(stuff ...any) {
	for _, s := range stuff {
		switch v := s.(type) {
		case time.Duration:
			av.Poll = v
		case string:
			av.Name = v
		case int:
			av.AVRing.Init(v)
			av.SSRC = uint32(uintptr(unsafe.Pointer(av)))
			av.等待上限 = time.Duration(config.Global.SpeedLimit)
		case uint32:
			av.SampleRate = v
		case byte:
			av.DecoderConfiguration.PayloadType = v
		case IStream:
			av.Stream = v
		case SpesificTrack[T]:
			av.SpesificTrack = v
		}
	}
}

func (av *Media[T]) LastWriteTime() time.Time {
	return av.AVRing.RingBuffer.LastValue.Timestamp
}

func (av *Media[T]) Play(ctx context.Context, onMedia func(*AVFrame[T]) error) error {
	for ar := av.ReadRing(); ctx.Err() == nil; ar.MoveNext() {
		ap := ar.Read(ctx)
		if err := onMedia(ap); err != nil {
			// TODO: log err
			return err
		}
	}
	return ctx.Err()
}

func (av *Media[T]) ReadRing() *AVRing[T] {
	return util.Clone(av.AVRing)
}

func (av *Media[T]) GetDecoderConfiguration() DecoderConfiguration[T] {
	return av.DecoderConfiguration
}

func (av *Media[T]) CurrentFrame() *AVFrame[T] {
	return &av.Value
}
func (av *Media[T]) PreFrame() *AVFrame[T] {
	return av.LastValue
}

func (av *Media[T]) generateTimestamp(ts uint32) {
	av.Value.PTS = ts
	av.Value.DTS = ts
}

func (av *Media[T]) WriteAVCC(ts uint32, frame AVCCFrame) {
	curValue := &av.Value
	curValue.AbsTime = ts
	curValue.BytesIn += len(frame)
	curValue.AppendAVCC(frame)
	// av.Stream.Tracef("WriteAVCC:ts %d,cts %d,len %d", ts, cts, len(frame))
}

func (av *Media[T]) Flush() {
	curValue, preValue := &av.Value, av.LastValue
	// 补完RTP
	if config.Global.EnableRTP && len(curValue.RTP) == 0 {
		av.CompleteRTP(curValue)
	}
	// 补完AVCC
	if config.Global.EnableAVCC && len(curValue.AVCC) == 0 {
		av.CompleteAVCC(curValue)
	}
	if av.起始时间.IsZero() {
		curValue.DeltaTime = 0
		av.重置(curValue.AbsTime)
	} else if curValue.AbsTime == 0 {
		curValue.DeltaTime = (curValue.DTS - preValue.DTS) / 90
		curValue.AbsTime = preValue.AbsTime + curValue.DeltaTime
	} else {
		curValue.DeltaTime = curValue.AbsTime - preValue.AbsTime
	}
	av.Base.Flush(&curValue.BaseFrame)
	if av.等待上限 > 0 {
		av.控制流速(curValue.AbsTime)
	}
	av.Step()
}
