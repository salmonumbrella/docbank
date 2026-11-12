package store

import (
	"fmt"
	"time"

	"go.kenn.io/kit/civiltime"
)

type TimelineTick struct {
	Minute int
	Label  string
}

type TimelineDayBounds struct {
	Start, End time.Time
	Minutes    int
	Ticks      []TimelineTick
}

func timelineDayBounds(date civiltime.Date, loc *time.Location) (TimelineDayBounds, bool) {
	start, end, ok := civiltime.DayBounds(date, loc)
	if !ok {
		return TimelineDayBounds{}, false
	}
	bounds := TimelineDayBounds{Start: start, End: end, Minutes: int(end.Sub(start) / time.Minute)}
	counts := make(map[string]int)
	for minute := 0; minute < bounds.Minutes; minute++ {
		wall := start.Add(time.Duration(minute) * time.Minute).In(loc)
		if minute != 0 && (wall.Minute() != 0 || wall.Second() != 0) {
			continue
		}
		label := wall.Format("15:04")
		bounds.Ticks = append(bounds.Ticks, TimelineTick{Minute: minute, Label: label})
		counts[label]++
	}
	for i := range bounds.Ticks {
		tick := &bounds.Ticks[i]
		if counts[tick.Label] < 2 {
			continue
		}
		_, seconds := start.Add(time.Duration(tick.Minute) * time.Minute).In(loc).Zone()
		sign := "+"
		if seconds < 0 {
			sign = "−"
			seconds = -seconds
		}
		tick.Label += fmt.Sprintf(" %s%02d:%02d", sign, seconds/3600, seconds%3600/60)
	}
	return bounds, true
}

func timelineAxisMinute(start, instant time.Time) int {
	return int(instant.Sub(start) / time.Minute)
}
