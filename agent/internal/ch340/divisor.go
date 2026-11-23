package ch340

import "fmt"

const clkRate = 48000000

func clkDiv(ps, fact int) int { return 1 << (12 - 3*ps - fact) }

// divisor returns the prescaler/divisor register value for a baud rate (ch341_get_divisor in Linux).
func divisor(speed int) (uint16, error) {
	ps := 3
	for ; ps >= 0; ps-- {
		if speed > clkRate/(clkDiv(ps, 1)*512) {
			break
		}
	}
	if ps < 0 {
		return 0, fmt.Errorf("baud %d too low", speed)
	}
	fact := 1
	cd := clkDiv(ps, fact)
	div := clkRate / (cd * speed)
	if div < 9 || div > 255 {
		div /= 2
		cd *= 2
		fact = 0
	}
	if div < 2 {
		return 0, fmt.Errorf("baud %d too high", speed)
	}
	if 16*clkRate/(cd*div)-16*speed >= 16*speed-16*clkRate/(cd*(div+1)) {
		div++
	}
	if fact == 1 && div%2 == 0 {
		div /= 2
		fact = 0
	}
	return uint16((0x100-div)<<8 | fact<<2 | ps), nil
}
