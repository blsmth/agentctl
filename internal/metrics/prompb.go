package metrics

// prompb contains minimal Prometheus remote write v1 protobuf types with
// hand-rolled encoding. This avoids pulling the full prometheus/prometheus
// module (200+ transitive deps) just for prompb.
//
// Wire format follows the protobuf specification:
//   https://protobuf.dev/programming-guides/encoding/

import (
	"encoding/binary"
	"math"
)

// pbLabel corresponds to prompb.Label.
type pbLabel struct {
	Name  string
	Value string
}

// pbSample corresponds to prompb.Sample.
type pbSample struct {
	Value     float64
	Timestamp int64 // milliseconds since Unix epoch
}

// pbTimeSeries corresponds to prompb.TimeSeries.
type pbTimeSeries struct {
	Labels  []pbLabel
	Samples []pbSample
}

// pbWriteRequest corresponds to prompb.WriteRequest.
type pbWriteRequest struct {
	Timeseries []pbTimeSeries
}

// marshal encodes wr as a protobuf binary message.
func (wr pbWriteRequest) marshal() []byte {
	var b []byte
	for _, ts := range wr.Timeseries {
		b = pbAppendMessage(b, 1, ts.marshal())
	}
	return b
}

func (ts pbTimeSeries) marshal() []byte {
	var b []byte
	for _, l := range ts.Labels {
		b = pbAppendMessage(b, 1, l.marshal())
	}
	for _, s := range ts.Samples {
		b = pbAppendMessage(b, 2, s.marshal())
	}
	return b
}

func (l pbLabel) marshal() []byte {
	var b []byte
	b = pbAppendString(b, 1, l.Name)
	b = pbAppendString(b, 2, l.Value)
	return b
}

func (s pbSample) marshal() []byte {
	var b []byte
	b = pbAppendDouble(b, 1, s.Value)
	b = pbAppendInt64(b, 2, s.Timestamp)
	return b
}

// pbAppendVarint appends a varint-encoded uint64 to b.
func pbAppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// pbAppendTag appends a field tag (field number + wire type).
func pbAppendTag(b []byte, fieldNum int, wireType int) []byte {
	return pbAppendVarint(b, uint64(fieldNum<<3|wireType))
}

// pbAppendString appends a length-delimited string field.
func pbAppendString(b []byte, fieldNum int, s string) []byte {
	b = pbAppendTag(b, fieldNum, 2)
	b = pbAppendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// pbAppendDouble appends a 64-bit IEEE 754 double field.
func pbAppendDouble(b []byte, fieldNum int, v float64) []byte {
	b = pbAppendTag(b, fieldNum, 1)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
	return append(b, buf[:]...)
}

// pbAppendInt64 appends a varint-encoded int64 field.
func pbAppendInt64(b []byte, fieldNum int, v int64) []byte {
	b = pbAppendTag(b, fieldNum, 0)
	return pbAppendVarint(b, uint64(v))
}

// pbAppendMessage appends a length-delimited embedded message field.
func pbAppendMessage(b []byte, fieldNum int, msg []byte) []byte {
	b = pbAppendTag(b, fieldNum, 2)
	b = pbAppendVarint(b, uint64(len(msg)))
	return append(b, msg...)
}
