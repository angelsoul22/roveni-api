package database

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq" // Driver de PostgreSQL
)

func InitDB() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dbUser := os.Getenv("DB_USER")
		dbPassword := os.Getenv("DB_PASSWORD")
		dbName := os.Getenv("DB_NAME")
		dbHost := os.Getenv("DB_HOST")
		if dbHost == "" {
			dbHost = "localhost"
		}
		dbPort := os.Getenv("DB_PORT")
		if dbPort == "" {
			dbPort = "5432"
		}
		if dbUser != "" && dbPassword != "" && dbName != "" {
			dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", dbUser, dbPassword, dbHost, dbPort, dbName)
		} else {
			return nil, fmt.Errorf("DATABASE_URL environment variable not set and fallback DB credentials incomplete")
		}
	}

	var db *sql.DB
	var err error

	for i := 0; i < 5; i++ {
		db, err = sql.Open("postgres", dsn)
		if err == nil {
			err = db.Ping()
			if err == nil {
				fmt.Println("¡Conectado exitosamente a PostgreSQL desde Go!")
				// Asegurar tablas existen
				if err := createTables(db); err != nil {
					return nil, fmt.Errorf("error creando tablas: %w", err)
				}
				return db, nil
			}
		}
		fmt.Println("Esperando a PostgreSQL...")
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("no se pudo conectar a la base de datos: %w", err)
}

func createTables(db *sql.DB) error {
	// Create users table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'organizador',
		created_at TIMESTAMPTZ DEFAULT now()
	)
	`); err != nil {
		return fmt.Errorf("error creating users table: %w", err)
	}

	// Add/update role constraint to support administrador, organizador, and cliente
	if _, err := db.Exec(`
	DO $$
	BEGIN
		ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
		ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('administrador','organizador','cliente'));
	END$$
	`); err != nil {
		return fmt.Errorf("error adding role constraint: %w", err)
	}

	// Create customer_profiles table for buyers
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS customer_profiles (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		full_name TEXT NOT NULL DEFAULT '',
		phone TEXT DEFAULT '',
		created_at TIMESTAMPTZ DEFAULT now(),
		UNIQUE (user_id)
	)
	`); err != nil {
		return fmt.Errorf("error creating customer_profiles table: %w", err)
	}

	// Create organizer_profiles table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS organizer_profiles (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		full_name TEXT NOT NULL DEFAULT '',
		phone TEXT,
		email TEXT,
		category TEXT,
		city TEXT,
		type_organizer TEXT DEFAULT 'organizador' CHECK (type_organizer IN ('organizador', 'promotor')),
		experience_area TEXT,
		document_id TEXT UNIQUE,
		created_at TIMESTAMPTZ DEFAULT now(),
		UNIQUE (user_id)
	)
	`); err != nil {
		return fmt.Errorf("error creating organizer_profiles table: %w", err)
	}

	// Create events table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS events (
		id SERIAL PRIMARY KEY,
		organizer_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		category TEXT NOT NULL,
		image_url TEXT,
		country TEXT NOT NULL,
		city TEXT NOT NULL,
		venue_address TEXT NOT NULL,
		event_date TEXT NOT NULL,
		doors_open_time TEXT NOT NULL,
		show_start_time TEXT NOT NULL,
		sale_start_date TEXT NOT NULL,
		sale_start_time TEXT NOT NULL,
		max_tickets_per_user INTEGER DEFAULT 4,
		status TEXT DEFAULT 'Publicado',
		currency TEXT DEFAULT 'USD',
		spotify_url TEXT DEFAULT '',
		apple_music_url TEXT DEFAULT '',
		youtube_url TEXT DEFAULT '',
		instagram_url TEXT DEFAULT '',
		tiktok_url TEXT DEFAULT '',
		created_at TIMESTAMPTZ DEFAULT now()
	);
	ALTER TABLE events ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'Publicado';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS currency TEXT DEFAULT 'USD';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS spotify_url TEXT DEFAULT '';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS apple_music_url TEXT DEFAULT '';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS youtube_url TEXT DEFAULT '';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS instagram_url TEXT DEFAULT '';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS tiktok_url TEXT DEFAULT '';
	ALTER TABLE events ADD COLUMN IF NOT EXISTS service_fee_percentage NUMERIC(5, 2) DEFAULT 20.00;
	`); err != nil {
		return fmt.Errorf("error creating events table: %w", err)
	}

	// Create ticket_categories table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS ticket_categories (
		id SERIAL PRIMARY KEY,
		event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		price NUMERIC(10, 2) NOT NULL,
		capacity INTEGER NOT NULL,
		description TEXT DEFAULT ''
	)
	`); err != nil {
		return fmt.Errorf("error creating ticket_categories table: %w", err)
	}

	// Create purchases table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS purchases (
		id SERIAL PRIMARY KEY,
		order_number TEXT NOT NULL UNIQUE,
		event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
		customer_name TEXT NOT NULL,
		customer_email TEXT NOT NULL,
		customer_phone TEXT DEFAULT '',
		subtotal NUMERIC(12, 2) DEFAULT 0.00,
		service_fee NUMERIC(12, 2) DEFAULT 0.00,
		total_amount NUMERIC(12, 2) NOT NULL,
		subtotal_usd NUMERIC(12, 2) DEFAULT 0.00,
		service_fee_usd NUMERIC(12, 2) DEFAULT 0.00,
		total_amount_usd NUMERIC(12, 2) DEFAULT 0.00,
		currency TEXT DEFAULT 'USD',
		event_currency TEXT DEFAULT 'USD',
		exchange_rate NUMERIC(12, 6) DEFAULT 1.0,
		stripe_payment_intent_id TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'completed',
		created_at TIMESTAMPTZ DEFAULT now()
	);
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS subtotal NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS service_fee NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS subtotal_usd NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS service_fee_usd NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS total_amount_usd NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS event_currency TEXT DEFAULT 'USD';
	ALTER TABLE purchases ADD COLUMN IF NOT EXISTS exchange_rate NUMERIC(12, 6) DEFAULT 1.0;
	`); err != nil {
		return fmt.Errorf("error creating purchases table: %w", err)
	}

	// Create purchase_items table
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS purchase_items (
		id SERIAL PRIMARY KEY,
		purchase_id INTEGER NOT NULL REFERENCES purchases(id) ON DELETE CASCADE,
		ticket_category_id INTEGER NOT NULL REFERENCES ticket_categories(id) ON DELETE CASCADE,
		ticket_name TEXT NOT NULL,
		price NUMERIC(12, 2) NOT NULL,
		price_usd NUMERIC(12, 2) DEFAULT 0.00,
		price_native NUMERIC(12, 2) DEFAULT 0.00,
		quantity INTEGER NOT NULL
	);
	ALTER TABLE purchase_items ADD COLUMN IF NOT EXISTS price_usd NUMERIC(12, 2) DEFAULT 0.00;
	ALTER TABLE purchase_items ADD COLUMN IF NOT EXISTS price_native NUMERIC(12, 2) DEFAULT 0.00;
	`); err != nil {
		return fmt.Errorf("error creating purchase_items table: %w", err)
	}

	// Create ticket_scans table for venue scanner validation
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS ticket_scans (
		id SERIAL PRIMARY KEY,
		order_number TEXT NOT NULL,
		ticket_serial TEXT NOT NULL UNIQUE,
		event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
		scanned_at TIMESTAMPTZ DEFAULT now(),
		scanned_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
		device_info TEXT DEFAULT ''
	);
	`); err != nil {
		return fmt.Errorf("error creating ticket_scans table: %w", err)
	}

	// Update legacy categories: change 'Teatro' or 'Teatro y Artes Escénicas' to 'Arte'
	if _, err := db.Exec(`
		UPDATE events 
		SET category = 'Arte' 
		WHERE category = 'Teatro' OR category = 'Teatro y Artes Escénicas'
	`); err != nil {
		fmt.Printf("Warning: failed to migrate Teatro categories: %v\n", err)
	}

	// Migration: Make purchase_items.ticket_category_id ON DELETE SET NULL to preserve purchases
	_, _ = db.Exec(`
		ALTER TABLE purchase_items DROP CONSTRAINT IF EXISTS purchase_items_ticket_category_id_fkey;
		ALTER TABLE purchase_items ALTER COLUMN ticket_category_id DROP NOT NULL;
		ALTER TABLE purchase_items ADD CONSTRAINT purchase_items_ticket_category_id_fkey FOREIGN KEY (ticket_category_id) REFERENCES ticket_categories(id) ON DELETE SET NULL;
	`)

	// Migration: Restore decremented capacity to total capacity for existing ticket_categories
	_, _ = db.Exec(`
		UPDATE ticket_categories tc
		SET capacity = tc.capacity + sub.total_sold
		FROM (
			SELECT pi.ticket_category_id, SUM(pi.quantity) as total_sold
			FROM purchase_items pi
			JOIN purchases p ON pi.purchase_id = p.id
			WHERE pi.ticket_category_id IS NOT NULL AND p.status = 'completed'
			GROUP BY pi.ticket_category_id
		) sub
		WHERE tc.id = sub.ticket_category_id AND tc.capacity < sub.total_sold;
	`)

	return nil
}
