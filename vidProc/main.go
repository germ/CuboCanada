package main

import (
	"fmt"
	"os"
	"math"
	"sort"
	"strings"
	"os/exec"
	"image/png"
	"github.com/germ/munsell"
	"github.com/google/skia-buildbot/perf/go/kmeans"
	colour "github.com/lucasb-eyer/go-colorful"
)

//TODO: Frame Drop
type Frames []Frame
type Frame struct {
	idx		int64					// Frames from zero
	col		colour.Color	// Avg colour
}
func main() {
	// Usage
	if len(os.Args) == 1 {
		fmt.Println("Usage: ./conv input.mp4 input2.mp4 ...")
		fmt.Println("Output: out/hexcolor.mp4")
		return
	}

	// Loop over all vids
	done := make(chan bool)
	for _, fileName:= range os.Args[1:] {
		go func(file string) {
			fmt.Println("Processing: ", file)

			// Load avg map, create if necessary
			frameData := loadData(file)
			_, wrongInter := frameData.getCenters()
			avgColors, clips := removeOutliers(wrongInter)

			for i, v := range clips {
				fmt.Printf("Encoding: %v (%v/%v)\n", fileName, i+1, len(clips))
				convName, _:= munsell.ConvertBetween(avgColors[i].Hex())
				exportClip(v[0].idx, v[len(v)-1].idx, file, fmt.Sprintf("%v.mp4", strings.Replace(convName, " ", "\ ", -1)))
			}

			done <- true
		}(fileName)
	}

	for i := 0; i < len(os.Args)-1; i++ {
		<- done
	}
}

// Data Ingress/Egress
func loadData(fileName string) (ret Frames) {
	// Check from prev run
	f, err := os.Open(fileName+".png")

	// Could not find, create
	if err != nil {
		workDir := fileName+"-frame"
		// Make working dir
		err = exec.Command("mkdir", workDir).Run()

		// Explode frames
		err = exec.Command("ffmpeg", "-i", fileName, "-vf", "scale=1:1", workDir+"/%010d.png").Run()
		if err != nil { fmt.Println(err) }

		// Create frame -> avg mapping
		err = exec.Command("convert", "+append", workDir+"/*.png", fileName+".png").Run()
		if err != nil { fmt.Println(err) }

		// Clean up exploded data
		err = exec.Command("/bin/sh", "-c", "rm", "-r", workDir).Run()
		if err != nil { fmt.Println(err) }

		// Open created file or reading
		f, err = os.Open(fileName+".png")
		if err != nil { fmt.Println(err) }
	}

	// Load image data
	img, err := png.Decode(f)
	if err != nil { errOut(err) }

	// Fill struct and append to return
	for i := 0; i < img.Bounds().Max.X; i++ {
		v := Frame {
			idx: int64(i),
			col: colour.MakeColor(img.At(i, 0)),
		}
		ret = append(ret, v)
	}

	fmt.Println("Data successfully loaded!")
	return
}
func exportClip(fStart, fEnd int64, source string, out string) {
	tStart := fmt.Sprintf("%v.%03v", fStart/60, int((float64(fStart % 60)/60.0)*1000.0))
	tEnd	 := fmt.Sprintf("%v.%03v", fEnd/60, int((float64(fEnd % 60)/60.0)*1000.0))

	exec.Command("mkdir", "out").Run()
	exec.Command("ffmpeg", "-y", "-ss", tStart, "-i", source, "-c", "copy", "-to", tEnd, "out/"+out).Run()
	fmt.Println("ffmpeg", "-y", "-ss", tStart, "-i", source, "-c", "copy", "-to", tEnd, "out/"+out)
}

// Remove sections that deviate from central color.
// Use analagous color (+-30deg from centeroid)
func removeOutliers(clips []Frames) (avgCol []colour.Color, ret []Frames) {
	for _, clip := range clips {
		// Calc hue, comp to every frame
		// Drop other frames
		clipHue, _, _ := clip[0].col.Hsv()
		var valids Frames
		for _, f := range clip {
			fHue, _, _ := f.col.Hsv()
			if math.Abs(clipHue - fHue) < 40.0 {
				valids = append(valids, f)
			}
		}

		// Sort, checking and splitting if over 4sec
		sort.Sort(valids)

		startOfRun := 0
		for i, v := range valids {
			// Looking for sequential frames
			if i != 0 && v.idx != (valids[i-1].idx + 1) {
				// Clip out if 4sec, discard otherwise
				if (valids[i-1].idx - valids[startOfRun].idx) > 60 * 5 {
					ret = append(ret, valids[startOfRun:i-1])
									
					// COPY PASTE. FORGIVE ME MARY
					var avgH, avgC, avgL float64
					for _, v := range valids[startOfRun : i-1] {
						h,c,l := v.col.Hcl()
						avgH += h
						avgC += c
						avgL += l
					}
					avgH = avgH / float64(i-(1+startOfRun))
					avgC = avgC / float64(i-(1+startOfRun))
					avgL = avgL / float64(i-(1+startOfRun))
					avgCol = append(avgCol, colour.Hcl(avgH, avgC, avgL))

					startOfRun = i
				}
			}
		}
	}

	fmt.Println("Got Cands: ", len(ret))
	return
}

// K M E A N S
// Auto Calc proper k val and find clusters
func (f Frames) getCenters() (centerFrames []Frame, groupings []Frames) {
	i, zeroCount, pErr := 1, 0, 99999999.00
	var centers []kmeans.Centroid
	var obs 		[][]kmeans.Clusterable

	// Check for manual param, otherwise calc sections
	// Tries to minimize error
	for ; zeroCount < 3; i++ {
		var nErr float64
		centers, obs, nErr = f.Kmeans(i)
		if (pErr-nErr < 0.00) {
			centers, obs, nErr = f.Kmeans(i-1)
			break
		}

		fmt.Println(i, ":", pErr-nErr)
		pErr = nErr
	}

	// Typecast and ret
	for _, v := range centers {
		centerFrames = append(centerFrames, v.(Frame))
	}
	for _, group := range obs {
		var f Frames
		for _, v := range group {
			f = append(f, v.(Frame))
		}

		groupings = append(groupings, f)
	}

	return
}
func (f Frames) Kmeans(k int) ([]kmeans.Centroid , [][]kmeans.Clusterable, float64) {
	// Convert to workable data
	var obs []kmeans.Clusterable
	for _, v := range f {
		obs = append(obs, v.AsClusterable())
	}

	// Initial sample every four seconds
	var init []kmeans.Centroid
	for i := 0; i < len(obs); i += len(obs)/k {
		init = append(init, f[i])
	}

	var errors float64
	centers, groupings := kmeans.KMeans(obs, init, k, 10, calculateCentroid)
	for i, v := range(groupings) {
		errors += kmeans.TotalError(v, []kmeans.Centroid{centers[i]})
	}

	return centers, groupings, errors
}
func (f Frame) AsClusterable() kmeans.Clusterable {
	return f
}
func (f Frame) Distance (c kmeans.Clusterable) float64 {
	o := c.(Frame)

	// Assume 60fps, want under 30s apart
	// TODO: Tune this
	return f.col.DistanceLab(o.col) * math.Abs((float64(f.idx) - float64(o.idx))/(60.0*2.0))
}
func calculateCentroid(mem []kmeans.Clusterable) kmeans.Centroid {
	var avgIdx, avgH, avgC, avgL float64
	for _, v := range(mem) {
		avgIdx += float64(v.(Frame).idx)
		h,c,l := v.(Frame).col.Hcl()
		avgH += h
		avgC += c
		avgL += l
	}
	avgIdx = avgIdx / float64(len(mem))
	avgH = avgH / float64(len(mem))
	avgC = avgC / float64(len(mem))
	avgL = avgL / float64(len(mem))
	avgColor := colour.Hcl(avgH, avgC, avgL)

	return Frame{idx: int64(avgIdx), col: avgColor}
}
func errOut(e error) {
	panic(e)
}

// Note: Sorting a frame object merges the centroid back into
// the other data. After kmeans is run, the first frame is the 
// centroid
func (f Frames) Len() int { return len(f) }
func (f Frames) Swap(i, j int) { f[i], f[j] = f[j], f[i] }
func (f Frames) Less(i, j int) bool { return f[i].idx < f[j].idx }