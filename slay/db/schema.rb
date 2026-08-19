# This file is auto-generated from the current state of the database. Instead
# of editing this file, please use the migrations feature of Active Record to
# incrementally modify your database, and then regenerate this schema definition.
#
# This file is the source Rails uses to define your schema when running `bin/rails
# db:schema:load`. When creating a new database, `bin/rails db:schema:load` tends to
# be faster and is potentially less error prone than running all of your
# migrations from scratch. Old migrations may fail to apply correctly if those
# migrations use external dependencies or application code.
#
# It's strongly recommended that you check this file into your version control system.

ActiveRecord::Schema[8.1].define(version: 2026_08_10_000000) do
  # These are extensions that must be enabled in order to support this database
  enable_extension "pg_catalog.plpgsql"

  create_table "batches", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.integer "expected_discs"
    t.string "name"
    t.boolean "open", default: true, null: false
    t.string "ordering", default: "default", null: false
    t.integer "season_from"
    t.integer "season_to"
    t.bigint "series_id"
    t.datetime "updated_at", null: false
    t.index ["open"], name: "index_batches_on_open"
    t.index ["series_id"], name: "index_batches_on_series_id"
  end

  create_table "bridge_cursors", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.bigint "last_event_id", default: 0, null: false
    t.string "name", null: false
    t.datetime "seen_at"
    t.datetime "updated_at", null: false
    t.index ["name"], name: "index_bridge_cursors_on_name", unique: true
  end

  create_table "candidates", force: :cascade do |t|
    t.float "confidence", default: 0.0, null: false
    t.datetime "created_at", null: false
    t.bigint "disc_id", null: false
    t.integer "disc_number"
    t.string "kind"
    t.string "net", null: false
    t.string "provider"
    t.string "provider_id"
    t.integer "season"
    t.string "title"
    t.datetime "updated_at", null: false
    t.text "why"
    t.integer "year"
    t.index ["disc_id", "confidence"], name: "index_candidates_on_disc_id_and_confidence"
    t.index ["disc_id"], name: "index_candidates_on_disc_id"
  end

  create_table "disc_titles", force: :cascade do |t|
    t.integer "audio_count", default: 0, null: false
    t.integer "chapters", default: 0, null: false
    t.datetime "created_at", null: false
    t.bigint "disc_id", null: false
    t.integer "duration_seconds", default: 0, null: false
    t.string "encoded_path"
    t.integer "episode_number"
    t.string "episode_title"
    t.integer "index", null: false
    t.string "rip_path"
    t.string "role", default: "unknown", null: false
    t.integer "season_number"
    t.string "source_file"
    t.integer "subtitle_count", default: 0, null: false
    t.string "thumbnail_path"
    t.datetime "updated_at", null: false
    t.index ["disc_id", "index"], name: "index_disc_titles_on_disc_id_and_index", unique: true
    t.index ["disc_id", "season_number", "episode_number"], name: "index_disc_titles_on_episode", where: "(episode_number IS NOT NULL)"
    t.index ["disc_id"], name: "index_disc_titles_on_disc_id"
  end

  create_table "discs", force: :cascade do |t|
    t.boolean "blocked", default: false, null: false
    t.string "blocked_reason"
    t.datetime "confirmed_at"
    t.string "confirmed_kind"
    t.integer "confirmed_season"
    t.string "confirmed_title"
    t.integer "confirmed_year"
    t.datetime "created_at", null: false
    t.string "disc_name"
    t.string "disc_type"
    t.string "fingerprint", null: false
    t.boolean "pal", default: false, null: false
    t.string "provider"
    t.string "provider_id"
    t.string "read_path"
    t.string "rip_dir"
    t.datetime "ripped_at"
    t.string "status", default: "rough", null: false
    t.integer "total_seconds", default: 0, null: false
    t.datetime "updated_at", null: false
    t.string "volume_label"
    t.index ["fingerprint"], name: "index_discs_on_fingerprint", unique: true
    t.index ["status"], name: "index_discs_on_status"
  end

  create_table "episode_claims", force: :cascade do |t|
    t.bigint "batch_id", null: false
    t.datetime "created_at", null: false
    t.bigint "disc_title_id", null: false
    t.bigint "episode_id", null: false
    t.datetime "updated_at", null: false
    t.index ["batch_id", "episode_id"], name: "index_episode_claims_on_batch_id_and_episode_id", unique: true
    t.index ["batch_id"], name: "index_episode_claims_on_batch_id"
    t.index ["disc_title_id"], name: "index_episode_claims_on_disc_title_id", unique: true
    t.index ["episode_id"], name: "index_episode_claims_on_episode_id"
  end

  create_table "episodes", force: :cascade do |t|
    t.date "aired_on"
    t.datetime "created_at", null: false
    t.string "name"
    t.integer "number", null: false
    t.string "ordering", default: "default", null: false
    t.integer "runtime_seconds"
    t.bigint "season_id", null: false
    t.datetime "updated_at", null: false
    t.index ["season_id", "number", "ordering"], name: "index_episodes_on_season_id_and_number_and_ordering", unique: true
    t.index ["season_id"], name: "index_episodes_on_season_id"
  end

  create_table "movies", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "provider", null: false
    t.string "provider_id", null: false
    t.integer "runtime_seconds"
    t.string "title", null: false
    t.datetime "updated_at", null: false
    t.integer "year"
    t.index ["provider", "provider_id"], name: "index_movies_on_provider_and_provider_id", unique: true
  end

  create_table "placements", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.bigint "disc_title_id", null: false
    t.datetime "linked_at"
    t.text "nfo"
    t.string "path", null: false
    t.datetime "updated_at", null: false
    t.index ["disc_title_id"], name: "index_placements_on_disc_title_id", unique: true
    t.index ["path"], name: "index_placements_on_path", unique: true
  end

  create_table "provider_cache", force: :cascade do |t|
    t.jsonb "body", default: {}, null: false
    t.string "cache_key", null: false
    t.datetime "created_at", null: false
    t.string "endpoint", null: false
    t.datetime "fetched_at", null: false
    t.string "provider", null: false
    t.datetime "updated_at", null: false
    t.index ["provider", "endpoint", "cache_key"], name: "index_provider_cache_on_lookup", unique: true
  end

  create_table "seasons", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.integer "number", null: false
    t.bigint "series_id", null: false
    t.datetime "updated_at", null: false
    t.index ["series_id", "number"], name: "index_seasons_on_series_id_and_number", unique: true
    t.index ["series_id"], name: "index_seasons_on_series_id"
  end

  create_table "series", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.integer "first_air_year"
    t.string "name", null: false
    t.string "provider", null: false
    t.string "provider_id", null: false
    t.datetime "updated_at", null: false
    t.index ["provider", "provider_id"], name: "index_series_on_provider_and_provider_id", unique: true
  end

  add_foreign_key "batches", "series"
  add_foreign_key "candidates", "discs"
  add_foreign_key "disc_titles", "discs"
  add_foreign_key "episode_claims", "batches"
  add_foreign_key "episode_claims", "disc_titles"
  add_foreign_key "episode_claims", "episodes"
  add_foreign_key "episodes", "seasons"
  add_foreign_key "placements", "disc_titles"
  add_foreign_key "seasons", "series"
end
