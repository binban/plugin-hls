package hls

import (
	"bytes"
	"container/ring"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/Monibuca/engine/v3"
	"github.com/Monibuca/utils/v3"
	"github.com/Monibuca/utils/v3/codec"
	"github.com/Monibuca/utils/v3/codec/mpegts"
)

var memoryTs sync.Map

func writeHLS(r *Stream) {
	var err error
	var hls_fragment int64       // hls fragment
	var hls_segment_count uint32 // hls segment count
	var vwrite_time uint32
	var video_cc, audio_cc uint16
	var outStream = Subscriber{ID: "HLSWriter", Type: "HLS"}

	var ring = ring.New(config.Window + 1)

	if err = outStream.Subscribe(r.StreamPath); err != nil {
		utils.Println(err)
		return
	}
	vt := outStream.WaitVideoTrack("h264")
	at := outStream.WaitAudioTrack("aac")
	if err != nil {
		return
	}
	var asc codec.AudioSpecificConfig
	if at != nil {
		asc, err = decodeAudioSpecificConfig(at.ExtraData)
	}
	if err != nil {
		return
	}
	if config.Fragment > 0 {
		hls_fragment = config.Fragment * 1000
	} else {
		hls_fragment = 10000
	}

	hls_playlist := Playlist{
		Version:        3,
		Sequence:       0,
		Targetduration: int(hls_fragment / 666), // hlsFragment * 1.5 / 1000
	}

	hls_path := filepath.Join(config.Path, r.StreamPath)
	hls_m3u8_name := hls_path + ".m3u8"
	os.MkdirAll(hls_path, 0755)
	if err = hls_playlist.Init(hls_m3u8_name, false); err != nil {
		log.Println(err)
		return
	}

	hls_segment_data := &bytes.Buffer{}
	outStream.OnVideo = func(ts uint32, pack *VideoPack) {
		packet, err := VideoPacketToPES(ts, pack.NALUs, vt.ExtraData.NALUs[0], vt.ExtraData.NALUs[1])
		if err != nil {
			return
		}
		if pack.IDR {
			// 当前的时间戳减去上一个ts切片的时间戳
			if int64(ts-vwrite_time) >= hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)

				timeNow := time.Now()
				time1 := timeNow.Format("060102")         //yyMMdd
				tsFilename := timeNow.Format("150405.ts") //HHmmss
				hls_path_day := filepath.Join(hls_path, time1)
				os.MkdirAll(hls_path_day, 0755)

				//创建回放的点播文件
				hls_m3u8_record := filepath.Join(hls_path_day, "record.m3u8")
				if _, errA := os.Stat(hls_m3u8_record); os.IsNotExist(errA) {
					if errB := hls_playlist.Init(hls_m3u8_record, true); errB != nil {
						log.Printf("创建点播文件失败%s。", hls_m3u8_record)
						log.Println(errB)
					}
				}

				//tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"

				tsData := hls_segment_data.Bytes()
				tsFilePath := filepath.Join(hls_path_day, tsFilename)
				if config.EnableWrite {
					if err = writeHlsTsSegmentFile(tsFilePath, tsData); err != nil {
						return
					}
				}
				if config.EnableMemory {
					ring.Value = tsFilePath
					memoryTs.Store(tsFilePath, tsData)
					if ring = ring.Next(); ring.Value != nil && len(ring.Value.(string)) > 0 {
						memoryTs.Delete(ring.Value)
					}
				}
				inf := PlaylistInf{
					Duration: float64((ts - vwrite_time) / 1000),
					Title:    fmt.Sprintf("%s/%s/%s", filepath.Base(hls_path), time1, tsFilename),
				}

				if hls_segment_count >= uint32(config.Window) {
					if err = hls_playlist.UpdateInf(hls_m3u8_name, hls_m3u8_name+".tmp", inf); err != nil {
						return
					}
				} else {
					if err = hls_playlist.WriteInf(hls_m3u8_name, inf); err != nil {
						return
					}
				}

				//写入点播文件
				inf.Title = tsFilename
				_ = hls_playlist.WriteInf(hls_m3u8_record, inf)

				hls_segment_count++
				vwrite_time = ts
				hls_segment_data.Reset()
			}
		}

		frame := new(mpegts.MpegtsPESFrame)
		frame.Pid = 0x101
		frame.IsKeyFrame = pack.IDR
		frame.ContinuityCounter = byte(video_cc % 16)
		frame.ProgramClockReferenceBase = uint64(ts) * 90
		if err = mpegts.WritePESPacket(hls_segment_data, frame, packet); err != nil {
			return
		}

		video_cc = uint16(frame.ContinuityCounter)
	}
	outStream.OnAudio = func(ts uint32, pack *AudioPack) {
		var packet mpegts.MpegTsPESPacket
		if packet, err = AudioPacketToPES(ts, pack.Raw, asc); err != nil {
			return
		}

		frame := new(mpegts.MpegtsPESFrame)
		frame.Pid = 0x102
		frame.IsKeyFrame = false
		frame.ContinuityCounter = byte(audio_cc % 16)
		//frame.ProgramClockReferenceBase = 0
		if err = mpegts.WritePESPacket(hls_segment_data, frame, packet); err != nil {
			return
		}
		audio_cc = uint16(frame.ContinuityCounter)
	}
	outStream.Play(at, vt)
	if config.EnableMemory {
		ring.Do(memoryTs.Delete)
	}
}
