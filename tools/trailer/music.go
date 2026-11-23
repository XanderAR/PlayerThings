package main

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
)

// The trailer's soundtrack is synthesized here, so it's ours to use: 120 BPM
// (one bar = 2 s, so every 4-second scene cut lands on a bar line), A minor,
// Am-F-C-G. Pads and an arpeggio open, drums and bass drop in with the first
// card, a brighter lift carries the studios section, and the end card falls
// back to pads on a final chord.

const (
	sampleRate = 44100
	beatSec    = 0.5 // seconds at 120 BPM
	barSec     = 4 * beatSec
)

type song struct {
	length float64 // seconds
	drop   float64 // drums and bass come in
	lift   float64 // brighter section
	outro  float64 // drums stop
}

// progression: chord tones (MIDI) per bar, cycling.
var progression = [][]int{
	{57, 60, 64}, // Am
	{53, 57, 60}, // F
	{55, 60, 64}, // C (G3 C4 E4)
	{55, 59, 62}, // G
}
var roots = []int{45, 41, 48, 43} // A2 F2 C3 G2

func midiHz(n int) float64 { return 440 * math.Pow(2, float64(n-69)/12) }

type buses struct{ pad, bass, arp, drums, fx []float64 }

func renderMusic(s song) []float32 {
	n := int(s.length * sampleRate)
	b := buses{make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)}
	rng := rand.New(rand.NewSource(7))
	at := func(t float64) int { return int(t * sampleRate) }

	bars := int(math.Ceil(s.length / barSec))
	for i := 0; i < bars; i++ {
		t0 := float64(i) * barSec
		chord := progression[i%4]
		root := roots[i%4]
		final := t0 >= s.outro+2*barSec // the last chord rings
		if t0 >= s.length {
			break
		}
		// Pads: darker in the intro, brighter in the lift.
		cut := 1400.0
		if t0 >= s.drop {
			cut = 2400
		}
		if t0 >= s.lift && t0 < s.outro {
			cut = 3600
		}
		dur := barSec + 0.05
		if final {
			chord, root, dur = progression[0], roots[0], s.length-t0
		}
		for _, note := range chord {
			pad(b.pad, at(t0), dur, midiHz(note), cut, 0.045)
		}
		if final {
			break
		}
		// Arpeggio: 16ths over chord tones an octave up.
		arpVol := 0.05
		if t0 < s.drop {
			arpVol = 0.032
		}
		pattern := []int{0, 1, 2, 1, 0, 2, 1, 2}
		for k := 0; k < 16; k++ {
			tt := t0 + float64(k)*beatSec/4
			note := chord[pattern[k%8]] + 12
			if t0 >= s.lift && t0 < s.outro && k%4 == 3 {
				note += 12 // sparkle in the lift
			}
			pluck(b.arp, at(tt), midiHz(note), arpVol)
		}
		// Bass: 8ths from the drop, whole notes in the outro.
		if t0 >= s.drop && t0 < s.outro {
			for k := 0; k < 8; k++ {
				bassNote(b.bass, at(t0+float64(k)*beatSec/2), 0.2, midiHz(root), 0.2)
			}
		} else if t0 >= s.outro {
			bassNote(b.bass, at(t0), barSec, midiHz(root), 0.16)
		}
		// Drums.
		if t0 >= s.drop && t0 < s.outro {
			for k := 0; k < 4; k++ {
				kick(b.drums, at(t0+float64(k)*beatSec), 0.85)
				if k == 1 || k == 3 {
					clap(b.drums, at(t0+float64(k)*beatSec), 0.3, rng)
				}
			}
			for k := 0; k < 8; k++ {
				tt := t0 + float64(k)*beatSec/2
				if k%2 == 1 {
					hat(b.drums, at(tt), 0.11, 0.09, rng) // open on the off-beats
				} else {
					hat(b.drums, at(tt), 0.025, 0.06, rng)
				}
			}
			if t0 >= s.lift {
				for k := 0; k < 16; k += 2 {
					hat(b.drums, at(t0+float64(k+1)*beatSec/4), 0.02, 0.035, rng)
				}
			}
		}
	}
	// Build-up into the drop and the lift: a rising noise sweep and a snare roll.
	riser(b.fx, at(s.drop-2*beatSec*2), at(s.drop), 0.16, rng)
	for k := 0; k < 8; k++ {
		tt := s.drop - barSec + float64(k)*beatSec/2
		if k >= 4 {
			clap(b.drums, at(tt+beatSec/4), 0.08+0.03*float64(k-4), rng)
		}
		clap(b.drums, at(tt), 0.08+0.025*float64(k), rng)
	}
	riser(b.fx, at(s.lift-beatSec*2), at(s.lift), 0.12, rng)
	for _, t := range []float64{s.drop, s.lift, s.outro} {
		impact(b.fx, at(t), 0.55, rng)
	}

	// Pump the pads, bass and arp under the kick (sidechain feel).
	for i := range b.pad {
		t := float64(i) / sampleRate
		if t < s.drop || t >= s.outro {
			continue
		}
		since := math.Mod(t-s.drop, beatSec)
		g := 1 - 0.42*math.Exp(-since/0.11)
		b.pad[i] *= g
		b.bass[i] *= g
		b.arp[i] *= 0.6 + 0.4*g
	}

	// Space: a ping-pong echo on the arp and a small reverb on pads and arp.
	left, right := make([]float64, n), make([]float64, n)
	echo := at(beatSec * 3 / 4)
	for i := 0; i < n; i++ {
		l, r := b.arp[i], b.arp[i]
		if i >= echo {
			l += 0.34 * right[i-echo] * 0.9
			r += 0.34 * left[i-echo] * 0.9
		}
		left[i], right[i] = l, r
	}
	send := make([]float64, n)
	for i := range send {
		send[i] = b.pad[i]*0.6 + (left[i]+right[i])*0.25
	}
	wetL, wetR := reverb(send, 0), reverb(send, 23)

	out := make([]float32, 2*n)
	peak := 0.0
	mix := make([]float64, 2*n)
	for i := 0; i < n; i++ {
		common := b.pad[i] + b.bass[i] + b.drums[i] + b.fx[i]
		mix[2*i] = common + left[i]*0.8 + wetL[i]*0.35
		mix[2*i+1] = common + right[i]*0.8 + wetR[i]*0.35
		for _, v := range mix[2*i : 2*i+2] {
			peak = math.Max(peak, math.Abs(v))
		}
	}
	gain := 1.15 / math.Max(peak, 1e-6)
	for i := range mix {
		t := float64(i/2) / sampleRate
		v := math.Tanh(mix[i]*gain) * 0.9
		if t < 0.03 {
			v *= t / 0.03
		}
		if t > s.length-1.4 {
			v *= math.Max(0, (s.length-t)/1.4)
		}
		out[i] = float32(v)
	}
	return out
}

// ----- instruments -----

func saw(phase float64) float64 { return 2*(phase-math.Floor(phase)) - 1 }

func pad(buf []float64, start int, dur, hz, cutoff, vol float64) {
	n := int(dur * sampleRate)
	release := int(0.6 * sampleRate)
	lp := newLowpass(cutoff)
	detune := []float64{-0.0045, 0, 0.0052}
	phases := []float64{0.1, 0.4, 0.7}
	for i := 0; i < n+release && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		env := math.Min(1, t/0.35)
		if i > n {
			env *= 1 - float64(i-n)/float64(release)
		}
		v := 0.0
		for k, d := range detune {
			phases[k] += hz * (1 + d) / sampleRate
			v += saw(phases[k])
		}
		buf[start+i] += lp.next(v/3) * env * vol
	}
}

func pluck(buf []float64, start int, hz, vol float64) {
	n := int(0.32 * sampleRate)
	phase := 0.0
	lp := newLowpass(3200)
	for i := 0; i < n && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		lp.set(700 + 2600*math.Exp(-t/0.06))
		phase += hz / sampleRate
		v := saw(phase)*0.7 + math.Sin(2*math.Pi*phase)*0.3
		buf[start+i] += lp.next(v) * math.Exp(-t/0.11) * vol
	}
}

func bassNote(buf []float64, start int, dur, hz, vol float64) {
	n := int(dur * sampleRate)
	rel := int(0.05 * sampleRate)
	phase := 0.0
	lp := newLowpass(420)
	for i := 0; i < n+rel && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		env := 0.55 + 0.45*math.Exp(-t/0.08)
		if i > n {
			env *= 1 - float64(i-n)/float64(rel)
		}
		if i < 40 {
			env *= float64(i) / 40
		}
		phase += hz / sampleRate
		v := saw(phase)*0.6 + math.Sin(2*math.Pi*phase*0.5)*0.6 // saw plus sub
		buf[start+i] += lp.next(v) * env * vol
	}
}

func kick(buf []float64, start int, vol float64) {
	n := int(0.4 * sampleRate)
	phase := 0.0
	for i := 0; i < n && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		hz := 46 + 110*math.Exp(-t/0.035)
		phase += hz / sampleRate
		v := math.Sin(2*math.Pi*phase) * math.Exp(-t/0.16)
		if i < 60 {
			v += (1 - float64(i)/60) * 0.35 // click
		}
		buf[start+i] += v * vol
	}
}

func clap(buf []float64, start int, vol float64, rng *rand.Rand) {
	n := int(0.22 * sampleRate)
	hp := newLowpass(900)
	lp := newLowpass(4200)
	for i := 0; i < n && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		env := math.Exp(-t / 0.07)
		for _, burst := range []float64{0.0, 0.011, 0.022} { // three quick hits
			if t >= burst && t < burst+0.01 {
				env = math.Max(env, 0.9)
			}
		}
		noise := rng.Float64()*2 - 1
		band := lp.next(noise) - hp.next(noise)
		buf[start+i] += band * env * vol * 1.6
	}
}

func hat(buf []float64, start int, decay, vol float64, rng *rand.Rand) {
	n := int((decay*6 + 0.01) * sampleRate)
	lp := newLowpass(7000)
	for i := 0; i < n && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		noise := rng.Float64()*2 - 1
		buf[start+i] += (noise - lp.next(noise)) * math.Exp(-t/decay) * vol
	}
}

func riser(buf []float64, from, to int, vol float64, rng *rand.Rand) {
	lp := newLowpass(300)
	for i := from; i < to && i < len(buf); i++ {
		if i < 0 {
			continue
		}
		p := float64(i-from) / float64(to-from)
		lp.set(300 + 9000*p*p)
		buf[i] += lp.next(rng.Float64()*2-1) * p * p * vol
	}
}

func impact(buf []float64, start int, vol float64, rng *rand.Rand) {
	n := int(1.6 * sampleRate)
	phase := 0.0
	lp := newLowpass(5000)
	for i := 0; i < n && start+i < len(buf); i++ {
		t := float64(i) / sampleRate
		phase += (40 + 30*math.Exp(-t/0.1)) / sampleRate
		boom := math.Sin(2*math.Pi*phase) * math.Exp(-t/0.5)
		crash := lp.next(rng.Float64()*2-1) * math.Exp(-t/0.45) * 0.35
		buf[start+i] += (boom + crash) * vol
	}
}

// ----- effects -----

type lowpass struct{ a, y float64 }

func newLowpass(cutoff float64) *lowpass { l := &lowpass{}; l.set(cutoff); return l }
func (l *lowpass) set(cutoff float64) {
	l.a = 1 - math.Exp(-2*math.Pi*cutoff/sampleRate)
}
func (l *lowpass) next(x float64) float64 { l.y += l.a * (x - l.y); return l.y }

// reverb is a small Schroeder reverb; spread offsets the delays per channel.
func reverb(in []float64, spread int) []float64 {
	out := make([]float64, len(in))
	for _, d := range []int{1557, 1617, 1491, 1422} {
		d += spread
		buf := make([]float64, d)
		idx, lp := 0, 0.0
		for i, x := range in {
			y := buf[idx]
			lp = y*0.7 + lp*0.3
			buf[idx] = x + lp*0.82
			idx = (idx + 1) % d
			out[i] += y * 0.25
		}
	}
	for _, d := range []int{225, 556} {
		d += spread / 2
		buf := make([]float64, d)
		idx := 0
		for i, x := range out {
			y := buf[idx]
			buf[idx] = x + y*0.5
			out[i] = y - x*0.5
			idx = (idx + 1) % d
		}
	}
	return out
}

// writeWAV saves 16-bit stereo PCM.
func writeWAV(path string, samples []float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data := len(samples) * 2
	hdr := []any{
		[4]byte{'R', 'I', 'F', 'F'}, uint32(36 + data), [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(2), uint32(sampleRate), uint32(sampleRate * 4), uint16(4), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, uint32(data),
	}
	for _, h := range hdr {
		if err := binary.Write(f, binary.LittleEndian, h); err != nil {
			return err
		}
	}
	pcm := make([]int16, len(samples))
	for i, v := range samples {
		pcm[i] = int16(math.Max(-1, math.Min(1, float64(v))) * 32767)
	}
	return binary.Write(f, binary.LittleEndian, pcm)
}
