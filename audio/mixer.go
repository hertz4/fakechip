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

	Ch        *[NumChans * 2]Channel  // Channels; pairs next to each other
	chans     *[NumChans](chan int32) // Data back from channel pairs
	Bpm       uint64                  // Song speed in beats per minute
	TickRate  uint64                  // Ticks per update
	TickSpeed uint64                  // Callback after this many ticks
	tickCount uint64                  // Counts down ticks until callback
}

const (
	PAIR_STEREO = iota
	PAIR_PM
	PAIR_AM
	PAIR_SYNC
)

// Internal channel data
type Channel struct {
	Wave            int    // Index of wave to use for wave function
	PairMode        int    // Pair mode. See above.
	Note            int32  // Midi note number
	Tune, TuneSlide int32  // Fine tuning, one note = 0x8000
	Vol, VolSlide   int32  // Pre-Volume that affects effects
	MVol, MVolSlide int32  // Mixer volume; after effects
	Len, Phase      uint64 // Length of wave and position in wave
	Period          uint64 // How much to increment phase for each point
	DelayTicks      uint64 // Length of delay in ticks
	DelayNote       int32  // Special delay timing used for guitar pluck
	delay           uint16 // Length of a delay effect in samples
	DelayLevel      int32  // Level at which to mix in delay effect
	Filter          uint16 // Rectangular filter added to delay

	hist     [1 << 16]int16 // 64kb of channel history
	histHead uint16         // Current history location
	delayAvg int32          // Rolling average tracker for delay
	out      int32          // Temporary storage val for channel output
}

func NewMixer(wave func(int, uint32) int16, seq func(*Mixer)) Mixer {
	m := Mixer{
		wave:      wave,
		seq:       seq,
		Ch:        new([NumChans * 2]Channel),
		chans:     new([NumChans]chan int32),
		Bpm:       120,
		TickRate:  24,
		TickSpeed: 6,
	}
	// Default params
	for i := range m.Ch {
		c := &m.Ch[i]
		c.MVol = 0x8000
		c.Note = 60
		c.Len = 0x10000 << 32
	}
	return m
}

func (m *Mixer) Start(output chan int16, srate uint64) {
	m.srate = srate

	// Start each channel pair wave output
	for i := range m.chans {
		m.chans[i] = make(chan int32)
		go m.startPair(i)
	}

	for {
		if m.count == m.nextTick {
			m.tick()
		}
		if m.tickCount >= m.TickSpeed {
			m.seq(m)
			m.tickCount = 0
		}
		// Total the mix
		var mix int32 = 0
		for i := range m.chans {
			mix += <-m.chans[i]
		}
		clamp16(&mix)
		output <- int16(mix)
		m.count++
	}

}

// This is ran multiple times per beat in order to update various data.
// It coincides with sequence callbacks.
func (m *Mixer) tick() {
	for i := range m.Ch {
		c := &m.Ch[i]

		// Sliding values
		c.Tune += c.TuneSlide
		c.Vol += c.VolSlide
		c.MVol += c.MVolSlide

		// Limit fine tune range indirectly so that note stays sane
		c.Note = c.Note + (c.Tune / 0x8000)
		c.Tune = c.Tune % 0x8000

		// Set delay amount
		switch {
		case c.DelayNote > 0:
			c.delay = uint16(float64(m.srate) /
				getNote(c.DelayNote, 0))
		case c.DelayTicks > 0:
			c.delay = uint16(c.DelayTicks * m.srate * 60 /
				m.Bpm / m.TickRate)
		default:
			c.delay = 0
		}

		// Cannot filter by 0
		if c.Filter == 0 {
			c.Filter = 1
		}

		// Set pitch
		rate := float64(c.Len / m.srate)
		c.Period = uint64(rate * getNote(c.Note, c.Tune))
	}
	m.nextTick = 60*m.srate/m.Bpm/m.TickRate + m.count
	m.tickCount++
}

// Run a pair of Chs
func (m *Mixer) startPair(i int) {
	phase := func(c *Channel) {
		c.Phase = (c.Phase + c.Period) % c.Len
	}
	filter := func(c *Channel) {
		// Get a wave out
		wave := int32(m.wave(c.Wave, uint32(c.Phase>>32)))
		c.out = int32((wave * c.Vol) >> 16)

		// Apply delay effect
		// Important that this is done first
		var delayStart uint16 = c.histHead - c.delay
		var delayEnd uint16 = delayStart - c.Filter
		c.delayAvg += int32(c.hist[delayStart]) / int32(c.Filter)
		c.delayAvg -= int32(c.hist[delayEnd]) / int32(c.Filter)
		clamp16(&c.delayAvg)
		c.out += c.delayAvg * c.DelayLevel >> 16

		// Store history for delay effect
		c.hist[c.histHead] = int16(c.out)
		c.histHead++
	}

	l := &m.Ch[i*2]
	r := &m.Ch[i*2+1]
	for {
		switch l.PairMode {
		case PAIR_PM:
			phase(l)
			filter(l)
			r.Phase = uint64(l.out+0x8000) << 32
			filter(r)
			m.chans[i] <- int32((r.out * l.MVol) >> 16)
			m.chans[i] <- int32((r.out * r.MVol) >> 16)
		default:
			phase(l)
			phase(r)
			filter(l)
			filter(r)
			m.chans[i] <- int32((l.out * l.MVol) >> 16)
			m.chans[i] <- int32((r.out * r.MVol) >> 16)
		}
	}
}

func getNote(note int32, tune int32) float64 {
	totalNote := float64(note) + float64(tune)/0x8000
	return math.Pow(2, (totalNote-60)/12.0) * 440
}

func (m *Mixer) OnPair(i int, op func(*Channel)) {
	op(&m.Ch[i*2])
	op(&m.Ch[i*2+1])
}

func clamp16(a *int32) {
	if *a < -0x8000 {
		*a = -0x8000
	} else if *a > 0x7fff {
		*a = 0x7fff
	}
}
