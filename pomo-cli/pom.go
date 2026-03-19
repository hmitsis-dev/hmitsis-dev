package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const dbFileName = "pomodoro.db"
const pidFileName = "pom.pid"

func getAppDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Error getting user home directory: %v", err)
	}
	appDir := fmt.Sprintf("%s/.pom", homeDir)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		err = os.Mkdir(appDir, 0755)
		if err != nil {
			log.Fatalf("Error creating application directory: %v", err)
		}
	}
	return appDir
}

func initDB() *sql.DB {
	appDir := getAppDir()
	dbPath := fmt.Sprintf("%s/%s", appDir, dbFileName)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal(err)
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_name TEXT NOT NULL,
		duration_minutes INTEGER NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL
	);
	`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatal(err)
	}

	return db
}

func saveTask(db *sql.DB, taskName string, duration int, startTime, endTime time.Time) {
	insertSQL := `INSERT INTO tasks (task_name, duration_minutes, start_time, end_time) VALUES (?, ?, ?, ?)`
	_, err := db.Exec(insertSQL, taskName, duration, startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))
	if err != nil {
		log.Fatal(err)
	}
}

// sendNotification sends a desktop notification on Linux and macOS, with a terminal bell as fallback.
func sendNotification(title, message string) {
	fmt.Println("\a")
	switch runtime.GOOS {
	case "linux":
		exec.Command("notify-send", title, message).Run()
	case "darwin":
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, message, title)
		exec.Command("osascript", "-e", script).Run()
	}
}

// runBreak runs a short break timer after a completed session.
func runBreak(duration int) {
	fmt.Printf("\nBreak time! %d minutes starting now...\n", duration)

	breakStart := time.Now()
	timer := time.NewTimer(time.Duration(duration) * time.Minute)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-timer.C:
		breakEnd := time.Now()
		sendNotification("Pomo", "Break over! Time to focus.")
		fmt.Println("Break over! Back to work.")

		db := initDB()
		defer db.Close()
		saveTask(db, "[break]", duration, breakStart, breakEnd)
	case <-sigChan:
		fmt.Println("\nBreak stopped.")
	}
}

func runTimer(taskName string, duration int, noBreak bool) {
	startTime := time.Now()

	timer := time.NewTimer(time.Duration(duration) * time.Minute)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-timer.C:
		// session completed
	case <-sigChan:
		fmt.Printf("\nTimer for '%s' was stopped.\n", taskName)
		return
	}

	endTime := time.Now()
	sendNotification("Pomo", fmt.Sprintf("Session '%s' complete!", taskName))
	fmt.Printf("Good job! You completed the session for '%s'!\n", taskName)

	db := initDB()
	defer db.Close()
	saveTask(db, taskName, duration, startTime, endTime)
	fmt.Printf("Task '%s' saved to the database.\n", taskName)

	if !noBreak {
		runBreak(5)
	}
}

func stopTimer() {
	appDir := getAppDir()
	pidPath := fmt.Sprintf("%s/%s", appDir, pidFileName)
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No active Pomodoro timer found in the background.")
			return
		}
		log.Fatalf("Error reading PID file: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		log.Fatalf("Invalid PID in file: %v", err)
	}

	os.Remove(pidPath)

	proc, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("Error finding process: %v", err)
	}

	err = proc.Signal(syscall.SIGTERM)
	if err != nil {
		log.Fatalf("Error signaling process: %v", err)
	}

	fmt.Printf("Successfully stopped Pomodoro timer (PID: %d).\n", pid)
}

func viewTasks() {
	db := initDB()
	defer db.Close()

	rows, err := db.Query("SELECT task_name, duration_minutes, start_time FROM tasks ORDER BY start_time DESC")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Println("\n--- Pomodoro Task History ---")
	foundTasks := false
	for rows.Next() {
		var taskName string
		var duration int
		var startTimeStr string
		err := rows.Scan(&taskName, &duration, &startTimeStr)
		if err != nil {
			log.Fatal(err)
		}

		startTime, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("- Task: '%s' | Duration: %d mins | Started: %s\n", taskName, duration, startTime.Format("2006-01-02 15:04:05"))
		foundTasks = true
	}

	if !foundTasks {
		fmt.Println("No tasks found in the database.")
	}
}

func displayStats() {
	db := initDB()
	defer db.Close()

	fmt.Println("\n--- Pomodoro Statistics ---")

	var totalSessions int
	err := db.QueryRow("SELECT COUNT(*) FROM tasks WHERE task_name != '[break]'").Scan(&totalSessions)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Total Pomodoro Sessions: %d\n", totalSessions)

	var totalDurationMinutes sql.NullInt64
	err = db.QueryRow("SELECT SUM(duration_minutes) FROM tasks WHERE task_name != '[break]'").Scan(&totalDurationMinutes)
	if err != nil {
		log.Fatal(err)
	}
	totalMinutes := 0
	if totalDurationMinutes.Valid {
		totalMinutes = int(totalDurationMinutes.Int64)
	}
	fmt.Printf("Total Focus Time: %d minutes\n", totalMinutes)

	var avgDuration sql.NullFloat64
	err = db.QueryRow("SELECT AVG(duration_minutes) FROM tasks WHERE task_name != '[break]'").Scan(&avgDuration)
	if err != nil {
		log.Fatal(err)
	}
	avg := 0.0
	if avgDuration.Valid {
		avg = avgDuration.Float64
	}
	fmt.Printf("Average Session Duration: %.2f minutes\n", avg)

	fmt.Println("\nMost Frequent Tasks:")
	rows, err := db.Query("SELECT task_name, COUNT(*) AS count FROM tasks WHERE task_name != '[break]' GROUP BY task_name ORDER BY count DESC LIMIT 5")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	foundFrequentTasks := false
	for rows.Next() {
		var taskName string
		var count int
		err := rows.Scan(&taskName, &count)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("- '%s': %d sessions\n", taskName, count)
		foundFrequentTasks = true
	}
	if !foundFrequentTasks {
		fmt.Println("No tasks recorded yet.")
	}
}

func main() {
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	task := startCmd.String("task", "", "The name of the task to work on.")
	timer := startCmd.Int("time", 25, "The duration of the Pomodoro session in minutes. Default is 25.")
	background := startCmd.Bool("background", true, "Run the timer in the background (default: true). Use --background=false to run interactively.")
	noBreak := startCmd.Bool("no-break", false, "Skip the automatic 5-minute break after the session.")
	isBackgroundChild := startCmd.Bool("is-background-child", false, "")

	viewCmd := flag.NewFlagSet("view", flag.ExitOnError)
	stopCmd := flag.NewFlagSet("stop", flag.ExitOnError)
	statsCmd := flag.NewFlagSet("stats", flag.ExitOnError)

	if len(os.Args) < 2 {
		fmt.Println("Usage: pom <command> [options]")
		fmt.Println("Commands: start, view, stop, stats")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		args := os.Args[2:]
		startCmd.Parse(args)

		taskName := *task
		duration := *timer

		if taskName == "" && len(startCmd.Args()) > 0 {
			taskName = startCmd.Args()[0]
			if len(startCmd.Args()) > 1 {
				if min, err := strconv.Atoi(startCmd.Args()[1]); err == nil {
					duration = min
				}
			}
		}

		if taskName == "" {
			fmt.Println("Task name is required. Usage: pom start <task> [minutes]")
			startCmd.PrintDefaults()
			os.Exit(1)
		}

		if *background && !*isBackgroundChild {
			childArgs := []string{"start", "--task", taskName, "--time", strconv.Itoa(duration), "--is-background-child"}
			if *noBreak {
				childArgs = append(childArgs, "--no-break")
			}
			cmd := exec.Command(os.Args[0], childArgs...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			err := cmd.Start()
			if err != nil {
				log.Fatalf("Failed to start background process: %v", err)
			}

			pid := cmd.Process.Pid
			appDir := getAppDir()
			pidPath := fmt.Sprintf("%s/%s", appDir, pidFileName)
			err = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644)
			if err != nil {
				log.Printf("Warning: Could not save PID file: %v\n", err)
			}

			fmt.Printf("Pomodoro timer for '%s' started (%d min). PID: %d\n", taskName, duration, pid)
			fmt.Println("Use 'pom stop' to end it early.")
			return
		}

		runTimer(taskName, duration, *noBreak)

	case "view":
		viewCmd.Parse(os.Args[2:])
		viewTasks()
	case "stop":
		stopCmd.Parse(os.Args[2:])
		stopTimer()
	case "stats":
		statsCmd.Parse(os.Args[2:])
		displayStats()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
