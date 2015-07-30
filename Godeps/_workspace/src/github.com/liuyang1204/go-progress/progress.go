// Package progress provides visibility & callback for IO progress.
package progress

import (
	"io"
)

// Wrap an io.Reader instance, add progress information.
type ProgressReader struct {
	io.Reader
	Size int64
	Finished int64
	OnProgress func(float32) //callback function when updating progress
}

func (c *ProgressReader) Read(p []byte) (n int, err error) {
    cBytes, err := c.Reader.Read(p)
    c.Finished = c.Finished + int64(cBytes)
	if err == nil && cBytes != 0 && c.OnProgress != nil {
		c.OnProgress(c.Progress())
	}
	return cBytes, err
}

func (c ProgressReader) Progress() (n float32) {
    return float32(c.Finished)/float32(c.Size)
}

// Wrap an io.Writer instance, add progress information.
type ProgressWriter struct {
	io.Writer
	Size int64
	Finished int64
	OnProgress func(float32) //callback function when updating progress
}

func (c *ProgressWriter) Write(p []byte) (n int, err error) {
    cBytes, err := c.Writer.Write(p)
    c.Finished = c.Finished + int64(cBytes)
	if err == nil && cBytes != 0 && c.OnProgress != nil {
		c.OnProgress(c.Progress())
	}
	return cBytes, err
}

func (c ProgressWriter) Progress() (n float32) {
    return float32(c.Finished)/float32(c.Size)
}

func NewProgressReader(input io.Reader, size int64) *ProgressReader {
	p := new(ProgressReader)
	p.Reader = input
	p.Size = size
	p.Finished = 0
	return p
}

func NewProgressWriter(output io.Writer, size int64) *ProgressWriter {
	p := new(ProgressWriter)
	p.Writer = output
	p.Size = size
	p.Finished = 0
	return p
}
