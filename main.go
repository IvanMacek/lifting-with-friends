package main

import (
	"encoding/csv"
	"fmt"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type CsvType int64

const (
	Comma = iota
	Semicolon
)

func readCsv(fileName string) ([][]string, CsvType, error) {
	csvType := CsvType(Comma)

	f, err := os.Open(fileName)
	if err != nil {
		return [][]string{}, csvType, err
	}

	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()

	if _, ok := err.(*csv.ParseError); ok {
		f.Seek(0, 0)
		r := csv.NewReader(f)
		r.Comma = ';'
		csvType = Semicolon
		records, err = r.ReadAll()
	}

	if err != nil {
		return [][]string{}, csvType, err
	}

	return records, csvType, nil
}

type LiftingSet struct {
	timestamp    time.Time
	exerciseName string
	weight       float64
	reps         int
	oneRepMax    float64
}

func (ls LiftingSet) calcOneRepMax() float64 {
	return ls.weight * (36 / (37 - float64(ls.reps)))
}

// Expected Apple input headers:
// Date,Workout Name,Duration,Exercise Name,Set Order,Weight,Reps,Distance,Seconds,Notes,Workout Notes,RPE
func parseAppleStrongCsvRecords(records [][]string) ([]LiftingSet, error) {
	noHeaderRecords := records[1:]
	cleanRecords := make([]LiftingSet, len(noHeaderRecords))

	for index, record := range noHeaderRecords {
		time, err := time.Parse("2006-01-02 15:04:05", record[0])
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Time failed at row %d", index))
			return []LiftingSet{}, err
		}

		weight, err := strconv.ParseFloat(record[5], 64)
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Weight failed at row %d", index))
			weight = 0
		}

		reps, err := strconv.ParseInt(record[6], 10, 32)
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Reps failed at row %d", index))
			reps = 0
		}

		ls := LiftingSet{
			timestamp:    time,
			exerciseName: record[3],
			weight:       weight,
			reps:         int(reps),
		}
		ls.oneRepMax = ls.calcOneRepMax()

		cleanRecords[index] = ls
	}

	return cleanRecords, nil
}

// Expected Android input headers:
// Date;Workout Name;Exercise Name;Set Order;Weight;Weight Unit;Reps;RPE;Distance;Distance Unit;Seconds;Notes;Workout Notes;Workout Duration
func parseAndroidStrongCsvRecords(records [][]string) ([]LiftingSet, error) {
	noHeaderRecords := records[1:]
	cleanRecords := make([]LiftingSet, len(noHeaderRecords))

	for index, record := range noHeaderRecords {
		time, err := time.Parse("2006-01-02 15:04:05", record[0])
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Time failed at row %d", index))
			return []LiftingSet{}, err
		}

		weight, err := strconv.ParseFloat(record[4], 64)
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Weight failed at row %d", index))
			weight = 0
		}

		reps, err := strconv.ParseInt(record[6], 10, 32)
		if err != nil {
			log.Println(fmt.Sprintf("Parsing Reps failed at row %d", index))
			reps = 0
		}

		ls := LiftingSet{
			timestamp:    time,
			exerciseName: record[2],
			weight:       weight,
			reps:         int(reps),
		}
		ls.oneRepMax = ls.calcOneRepMax()

		cleanRecords[index] = ls
	}

	return cleanRecords, nil
}

type ExerciseAggData struct {
	Timestamp    time.Time `json:"timestamp"`
	MaxWeight    float64   `json:"maxWeight"`
	MaxOneRepMax float64   `json:"maxOneRepMax"`
	TotalVolume  float64   `json:"totalVolume"`
}

type UserExerciseTimeSeries = map[string][]ExerciseAggData

func calculateExerciseTimeSeries(liftingSets []LiftingSet) UserExerciseTimeSeries {
	m := make(map[string]map[time.Time]ExerciseAggData)

	for _, ls := range liftingSets {
		if _, contains := m[ls.exerciseName]; !contains {
			m[ls.exerciseName] = make(map[time.Time]ExerciseAggData)
		}
		if _, contains := m[ls.exerciseName][ls.timestamp]; !contains {
			m[ls.exerciseName][ls.timestamp] = ExerciseAggData{
				Timestamp: ls.timestamp,
			}
		}

		data := m[ls.exerciseName][ls.timestamp]
		m[ls.exerciseName][ls.timestamp] = ExerciseAggData{
			Timestamp:    ls.timestamp,
			MaxWeight:    math.Max(data.MaxWeight, ls.weight),
			MaxOneRepMax: math.Max(data.MaxOneRepMax, ls.oneRepMax),
			TotalVolume:  data.TotalVolume + ls.weight*float64(ls.reps),
		}
	}

	// Drop the map
	m2 := make(map[string][]ExerciseAggData, len(m))

	for user, dataMap := range m {
		m2[user] = make([]ExerciseAggData, len(dataMap))

		index := 0
		for _, data := range dataMap {
			m2[user][index] = data
			index++
		}

		sort.Slice(m2[user], func(i, j int) bool {
			return m2[user][i].Timestamp.Before(m2[user][j].Timestamp)
		})
	}

	return m2
}

var storagePath = "storage/"
var userData map[string]UserExerciseTimeSeries

func loadStorage() {
	files, err := os.ReadDir(storagePath)
	if err != nil {
		log.Fatal(err)
		return
	}

	userData = make(map[string]UserExerciseTimeSeries)

	for _, file := range files {
		records, csvType, err := readCsv(path.Join(storagePath, file.Name()))
		if err != nil {
			log.Println(err)
			continue
		}

		var listingSets []LiftingSet
		if csvType == Comma {
			listingSets, err = parseAppleStrongCsvRecords(records)
		} else {
			listingSets, err = parseAndroidStrongCsvRecords(records)
		}

		if err != nil {
			log.Println(err)
			continue
		}

		userData[file.Name()] = calculateExerciseTimeSeries(listingSets)
	}
}

func main() {
	loadStorage()

	r := gin.Default()
	r.Use(gzip.Gzip(gzip.DefaultCompression))
	r.MaxMultipartMemory = 10 << 20 // 10 MiB
	r.LoadHTMLGlob("client/*.html")

	r.GET("/api/data", func(c *gin.Context) {
		c.JSON(http.StatusOK, userData)
	})

	r.POST("/upload", func(c *gin.Context) {
		username := c.PostForm("user")
		log.Println(username)

		file, _ := c.FormFile("file")
		log.Println(file.Filename)

		saveFilePath := filepath.Join(storagePath, username)
		c.SaveUploadedFile(file, saveFilePath)

		c.String(http.StatusOK, fmt.Sprintf("'%s' uploaded!", file.Filename))
	})

	r.Static("/assets", "client")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	r.Run()
}
