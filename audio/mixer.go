/* Does all of the work of the audio chip, which is really just a
 * mixer with added features.  Everything else is in this package is not
 * considered a part of the chip, but helps to use it.
 *
 * For most uint64's here, top 32 bits are used, bottom 32 are like a counter.
 * This should probably be altered to use all bits of uint32 and just cope
 * with the imprecision and limits.
 */

package audio

import "math"

// The number of channel pairs, or mixer chans
const NumChans int = 2

type Mixer struct {
	srate uint64                  // Sample rate
	wave  func(int, uint32) int16 // Function used for sound waves
	seq   func(*Mixer)

	count    uint64 // Point counter
	nextTick uint64 // Location of next tick in points

	Channel   *[NumChans * 2]Channel  // Channels; pairs next to each other
	chans     *[NumChans](chan int16) // Data back from channel pairs
	Bpm       uint64                  // Song speed in beats per minute
	TickRate  uint64                  // Ticks per update
	TickSpeed uint64                  // Callback after this many ticks
	tickCount uint64                  // Counts down ticks until callback
}

// Internal channel data
type Channel struct {
	Wave       int    // Index of wave to use for wave function
	Note       int    // Midi note number
	Len, Phase uint64 // Length of wave and position in wave
	Period     uint64 // How much to increment phase for each point
	update     bool   // Whether or not to recalculate values
}

func NewMixer(wave func(int, uint32) int16, seq func(*Mixer)) Mixer {
	return Mixer{
		wave:      wave,
		seq:       seq,
		Channel:   new([NumChans * 2]Channel),
		chans:     new([NumChans]chan int16),
		Bpm:       120,
		TickRate:  24,
		TickSpeed: 6,
	}
}

func (m *Mixer) Start(output chan int16, srate uint64) {
	m.srate = srate

	// Start each channel pair wave output
	for i := range m.chans {
		m.chans[i] = make(chan int16)
		go m.startPair(i)
	}

	// Run the mixer and ticking
	for {
		if m.count == m.nextTick {
			m.tick()
		}
		if m.tickCount >= m.TickSpeed {
			m.seq(m)
			m.tickCount = 0
		}
		var mix int32 = 0
		for i := range m.chans {
			mix += int32(<-m.chans[i])
		}
		output <- int16(mix >> 2)
		m.count++
	}

}

// This is ran multiple times per beat in order to update various data.
// It coincides with sequence callbacks.
func (m *Mixer) tick() {
	for i := range m.Channel {
		c := &m.Channel[i]
		c.Period = m.getPointPeriod(c.Len, c.Note)
	}
	m.nextTick = 60*m.srate/m.Bpm/m.TickRate + m.count
	m.tickCount++
}

// Run a pair of Channels
func (m *Mixer) startPair(i int) {
	l := &m.Channel[i*2]
	// basic test code
	l.Len = 0xffff << 32
	l.Note = 60
	for {
		l.Phase = (l.Phase + l.Period) % l.Len
		m.chans[i] <- m.wave(l.Wave, uint32(l.Phase>>32))
	}
}

// Calculate amount to add to phase to produce a given pitch
func (m *Mixer) getPointPeriod(len uint64, note int) uint64 {
	// Find point period for 1hz wave at given length
	rate := len / m.srate
	// Find desired pitch in hertz
	pitch := math.Pow(2, float64(note-60)/12.0) * 440
	return uint64(float64(rate) * pitch)
}
