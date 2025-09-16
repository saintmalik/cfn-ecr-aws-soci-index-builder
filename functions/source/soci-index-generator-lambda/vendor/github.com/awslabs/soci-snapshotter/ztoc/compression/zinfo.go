/*
   Copyright The Soci Snapshotter Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package compression

import (
	"fmt"
	"io"
)

type Zinfo interface {
	ExtractDataFromBuffer(compressedBuf []byte, uncompressedSize, uncompressedOffset Offset, spanID SpanID) ([]byte, error)
	ExtractDataFromFile(fileName string, uncompressedSize, uncompressedOffset Offset) ([]byte, error)
	Close()
	Bytes() ([]byte, error)
	MaxSpanID() SpanID
	SpanSize() Offset
	UncompressedOffsetToSpanID(offset Offset) SpanID
	StartCompressedOffset(spanID SpanID) Offset
	EndCompressedOffset(spanID SpanID, fileSize Offset) Offset
	StartUncompressedOffset(spanID SpanID) Offset
	EndUncompressedOffset(spanID SpanID, fileSize Offset) Offset
	VerifyHeader(r io.Reader) error
}

func newGzipZinfo(zinfoBytes []byte) (Zinfo, error) {
	return nil, fmt.Errorf("gzip compression not supported")
}

func newGzipZinfoFromFile(filename string, spanSize int64) (Zinfo, error) {
	return nil, fmt.Errorf("gzip compression not supported")
}

func NewZinfo(compressionAlgo string, zinfoBytes []byte) (Zinfo, error) {
	switch compressionAlgo {
	case Gzip:
		return newGzipZinfo(zinfoBytes)
	case Zstd:
		return nil, fmt.Errorf("not implemented: %s", Zstd)
	case Uncompressed, Unknown:
		return newTarZinfo(zinfoBytes)
	default:
		return nil, fmt.Errorf("unexpected compression algorithm: %s", compressionAlgo)
	}
}

func NewZinfoFromFile(compressionAlgo string, filename string, spanSize int64) (Zinfo, error) {
	switch compressionAlgo {
	case Gzip:
		return newGzipZinfoFromFile(filename, spanSize)
	case Zstd:
		return nil, fmt.Errorf("not implemented: %s", Zstd)
	case Uncompressed:
		return newTarZinfoFromFile(filename, spanSize)
	default:
		return nil, fmt.Errorf("unexpected compression algorithm: %s", compressionAlgo)
	}
}