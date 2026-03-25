package main

import (
	"encoding/csv"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/playwright-community/playwright-go"
	"gopkg.in/yaml.v3"
)

type Config struct {
	JwestID  string `yaml:"jwest_id"`
	Password string `yaml:"password"`
}

func loadConfig(path string) (Config, error) {
	var config Config
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	err = yaml.Unmarshal(data, &config)
	return config, err
}

func main() {
	headlessFlag := flag.Bool("headless", true, "Run browser in headless mode")
	outputFlag := flag.String("output", "history.csv", "Output CSV file path")
	configFlag := flag.String("config", "config.yaml", "Config YAML file path")
	flag.Parse()

	// Load credentials
	jwestID := os.Getenv("JWEST_ID")
	password := os.Getenv("JWEST_PASSWORD")

	if jwestID == "" || password == "" {
		config, err := loadConfig(*configFlag)
		if err == nil {
			if jwestID == "" {
				jwestID = config.JwestID
			}
			if password == "" {
				password = config.Password
			}
		}
	}

	if jwestID == "" || password == "" {
		log.Println("Error: J-WEST ID and password are not set.")
		log.Fatal("Please set them via environment variables (JWEST_ID, JWEST_PASSWORD) or config.yaml")
	}

	// Install playwright
	err := playwright.Install()
	if err != nil {
		log.Fatalf("Could not install playwright dependencies: %v", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("Could not start playwright: %v", err)
	}
	defer pw.Stop()

	launchOptions := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(*headlessFlag),
	}
	browser, err := pw.Chromium.Launch(launchOptions)
	if err != nil {
		log.Fatalf("Could not launch browser: %v", err)
	}
	defer browser.Close()

	storageStatePath := "state.json"
	needSave := false
	var context playwright.BrowserContext
	if _, err := os.Stat(storageStatePath); err == nil {
		// Load existing storage state
		context, err = browser.NewContext(playwright.BrowserNewContextOptions{StorageStatePath: playwright.String(storageStatePath)})
	} else {
		// No storage state file, create new context and will save after login
		context, err = browser.NewContext()
		needSave = true
	}
	if err != nil {
		log.Fatalf("Could not create context: %v", err)
	}
	defer context.Close()

	page, err := context.NewPage()

	if err != nil {
		log.Fatalf("Could not create page: %v", err)
	}

	log.Println("Navigating to login page...")
	_, err = page.Goto("https://icoca.jr-odekake.net/pc/mbicocausehistory.do")
	if err != nil {
		log.Fatalf("Could not navigate: %v", err)
	}

	// Wait for the login form to load
	err = page.Locator("#label-westerid").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})

	if err == nil {
		log.Println("Filling login information...")
		err = page.Locator("#label-westerid").Fill(jwestID)
		if err != nil {
			log.Fatalf("Could not fill WESTER ID: %v", err)
		}

		err = page.Locator("#textPassword").Fill(password)
		if err != nil {
			log.Fatalf("Could not fill password: %v", err)
		}

		// Try clicking the login button
		log.Println("Submitting login form...")
		loginBtn := page.Locator("button.c-btn__link")
		count, _ := loginBtn.Count()
		if count > 0 {
			err = loginBtn.First().Click()
			if err != nil {
				log.Fatalf("Could not click login button: %v", err)
			}
		} else {
			err = page.Locator("#textPassword").Press("Enter")
			if err != nil {
				log.Fatalf("Could not submit form: %v", err)
			}
		}
	} else {
		log.Println("Login page not detected. Proceeding to history extraction (site may be already authenticated or structure changed).")
	}

	// Save storage state after successful login if needed
	if needSave {
		if _, err := context.StorageState(storageStatePath); err != nil {
			log.Printf("Warning: could not save storage state: %v", err)
		} else {
			log.Println("Saved storage state to", storageStatePath)
		}
	}

	file, err := os.Create(*outputFlag)
	if err != nil {
		log.Fatalf("Could not create output file: %v", err)
	}
	defer file.Close()

	// Write UTF-8 BOM for Excel compatibility
	_, err = file.Write([]byte{0xEF, 0xBB, 0xBF})
	if err != nil {
		log.Printf("Warning: failed to write BOM: %v", err)
	}

	writer := csv.NewWriter(file)
	defer writer.Flush()

	log.Println("Checking for available months...")
	dropdown := page.Locator("select[name='ymref']")
	err = dropdown.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000), // 10s timeout
	})

	var months []string
	if err == nil {
		options, err := dropdown.Locator("option").All()
		if err == nil {
			for _, opt := range options {
				val, err := opt.GetAttribute("value")
				if err == nil && val != "" {
					months = append(months, val)
				}
			}
		}
	}

	if len(months) == 0 {
		// Fallback to a single iteration if dropdown is not found
		months = append(months, "")
	}

	isFirstTable := true
	totalRows := 0

	for _, monthVal := range months {
		if monthVal != "" {
			log.Printf("Fetching history for month: %s...\n", monthVal)
			dropdown = page.Locator("select[name='ymref']")
			dropdown.WaitFor(playwright.LocatorWaitForOptions{
				State:   playwright.WaitForSelectorStateVisible,
				Timeout: playwright.Float(10000), // 10s timeout
			})
			_, err = dropdown.SelectOption(playwright.SelectOptionValues{
				Values: playwright.StringSlice(monthVal),
			})
			if err != nil {
				log.Printf("Warning: Could not select month %s: %v", monthVal, err)
			}
		}

		searchBtn := page.Locator("input[alt='検索']")
		err = searchBtn.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(30000), // 30s timeout
		})

		if err == nil {
			log.Printf("Clicking 'Search' to display history for %s...", monthVal)
			err = searchBtn.First().Click()
			if err != nil {
				log.Printf("Warning: Could not click search button: %v", err)
			}
		} else {
			log.Println("No 'Search' button found. Assuming history table is directly accessible.")
		}

		log.Println("Waiting for history table to load...")
		err = page.Locator("table.typeE, table").First().WaitFor(playwright.LocatorWaitForOptions{
			State: playwright.WaitForSelectorStateVisible,
		})
		if err != nil {
			log.Printf("Could not find table for month %s. Skipping.", monthVal)
			continue
		}

		tables, err := page.Locator("table").All()
		if err != nil || len(tables) == 0 {
			log.Println("No tables found on page")
			continue
		}

		var bestTable playwright.Locator
		maxRows := 0

		for _, t := range tables {
			rows, err := t.Locator("tr").All()
			if err == nil && len(rows) > maxRows {
				maxRows = len(rows)
				bestTable = t
			}
		}

		if maxRows == 0 {
			log.Printf("Could not find any rows in the detected tables for month %s\n", monthVal)
			continue
		}

		rows, err := bestTable.Locator("tr").All()
		if err != nil {
			log.Printf("Could not get table rows: %v\n", err)
			continue
		}

		for idx, row := range rows {
			cells, err := row.Locator("td, th").All()
			if err != nil {
				continue
			}

			// Skip header row for subsequent tables to avoid duplicating the header in CSV
			if !isFirstTable && idx == 0 {
				continue
			}

			var rowData []string
			for _, cell := range cells {
				text, err := cell.InnerText()
				if err == nil {
					rowData = append(rowData, strings.TrimSpace(text))
				} else {
					rowData = append(rowData, "")
				}
			}
			if len(rowData) > 0 {
				err = writer.Write(rowData)
				if err != nil {
					log.Printf("Warning: could not write row to CSV: %v", err)
				} else {
					// Count data rows (excluding headers)
					if idx > 0 || isFirstTable {
						totalRows++
					}
				}
			}
		}
		isFirstTable = false
	}

	log.Printf("Successfully extracted %d total rows (incl. header) across %d month(s) and saved to %s", totalRows, len(months), *outputFlag)
}
