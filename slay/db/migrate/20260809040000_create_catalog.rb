# The catalog: what hellbox thinks each disc is.
#
# The split with the daemon's SQLite is deliberate and worth restating, because
# it is the thing most likely to erode. The daemon owns facts about bytes --
# whether a title verified, how many sectors could not be read, which encoder
# ran. This owns judgments about meaning -- which series a disc belongs to,
# which title is episode five, whether the OCR read "Doacters" or "Docaters".
#
# Facts are mirrored here for display and for reasoning across discs. They are
# never authoritative here: if this database and the rips tree disagree, the
# rips tree is right, because it is self-describing and this is an index.
class CreateCatalog < ActiveRecord::Migration[8.1]
  def change
    # ---- discs -------------------------------------------------------------

    create_table :discs do |t|
      # The daemon's fingerprint. The join between the two databases, and the
      # reason a disc reinserted next year is recognised in seconds.
      t.string :fingerprint, null: false
      t.string :volume_label

      # The disc's own name, where it has one -- only Blu-rays carry this, in
      # BDMV/META/DL/bdmt_eng.xml. Kept beside the volume label rather than
      # replacing it because they disagree and both are evidence:
      # "FIREFLY: DISC 1" against "FIREFLYUS_D1".
      t.string :disc_name

      t.string :disc_type            # dvd | bluray
      t.string :read_path            # native-dvd | native-bluray-aacs | decrypt-copy | makemkv
      t.string :rip_dir

      # A disc that cannot be ripped is still catalogued. Enumeration needs no
      # decryption on either format, so a BD+ Blu-ray arrives here complete --
      # name, episode count, runtimes, artwork -- and simply cannot be read.
      # It must appear in the interface as a known thing, never as a failure.
      t.boolean :blocked, null: false, default: false
      t.string  :blocked_reason

      # rough -> needs_review -> confirmed -> filed. See design 5.2.
      t.string :status, null: false, default: "rough"

      t.boolean :pal, null: false, default: false
      t.integer :total_seconds, null: false, default: 0
      t.datetime :ripped_at

      t.timestamps
    end
    add_index :discs, :fingerprint, unique: true
    add_index :discs, :status

    # ---- titles ------------------------------------------------------------

    create_table :disc_titles do |t|
      t.references :disc, null: false, foreign_key: true

      # hellbox's own 0-based index, matching title_NN.mkv.
      t.integer :index, null: false
      t.integer :duration_seconds, null: false, default: 0
      t.integer :chapters, null: false, default: 0

      # For a Blu-ray this is the playlist (00001.mpls); for a DVD the source
      # file. Without it a title cannot be read again without re-enumerating.
      t.string :source_file

      t.string :rip_path
      t.string :encoded_path
      t.string :thumbnail_path

      t.integer :audio_count, null: false, default: 0
      t.integer :subtitle_count, null: false, default: 0

      t.timestamps
    end
    add_index :disc_titles, %i[disc_id index], unique: true

    # ---- what the nets proposed --------------------------------------------

    # One row per net per disc. Kept rather than collapsed to a winner, because
    # a wrong name in the library has to be traceable to the evidence that
    # produced it -- and because agreement between two nets is worth more than
    # either being confident, which cannot be computed once they are discarded.
    create_table :candidates do |t|
      t.references :disc, null: false, foreign_key: true

      t.string :net, null: false     # label | bdmt | shape | menu | credits | provider | set | ifo
      t.string :title
      t.integer :year
      t.string :kind                 # movie | series
      t.integer :season
      t.integer :disc_number

      t.float :confidence, null: false, default: 0.0

      # The net's own account of itself, shown in the review queue. A number
      # with no reasoning behind it cannot be argued with.
      t.text :why

      t.timestamps
    end
    add_index :candidates, %i[disc_id confidence]

    # ---- provider records --------------------------------------------------

    create_table :series do |t|
      t.string :provider, null: false          # tmdb | tvdb
      t.string :provider_id, null: false
      t.string :name, null: false
      t.integer :first_air_year
      t.timestamps
    end
    add_index :series, %i[provider provider_id], unique: true

    create_table :seasons do |t|
      t.references :series, null: false, foreign_key: true
      t.integer :number, null: false
      t.timestamps
    end
    add_index :seasons, %i[series_id number], unique: true

    create_table :episodes do |t|
      t.references :season, null: false, foreign_key: true
      t.integer :number, null: false
      t.string :name
      t.integer :runtime_seconds
      t.date :aired_on

      # Providers disagree about ordering, and for some series it genuinely
      # matters -- Firefly's broadcast order is not its intended order, and
      # runtime alignment cannot possibly tell two orderings of the same
      # episodes apart. Which ordering was used is recorded, not assumed.
      t.string :ordering, null: false, default: "default"

      t.timestamps
    end
    add_index :episodes, %i[season_id number ordering], unique: true

    create_table :movies do |t|
      t.string :provider, null: false
      t.string :provider_id, null: false
      t.string :title, null: false
      t.integer :year
      t.integer :runtime_seconds
      t.timestamps
    end
    add_index :movies, %i[provider provider_id], unique: true

    # ---- batches: declaring a set before feeding it ------------------------

    # The largest simplification available to the hardest problem here. Say what
    # is coming and episode assignment stops being inference and becomes
    # consumption: not "what season is this and where does it start" but "which
    # four of these fourteen unclaimed episodes are these four titles".
    create_table :batches do |t|
      t.references :series, foreign_key: true
      t.string :name
      t.integer :season_from
      t.integer :season_to
      t.integer :expected_discs
      t.string :ordering, null: false, default: "default"

      # Open until closed by hand. Never auto-closed on a timer -- a box set can
      # take days to work through, and a batch expiring underneath someone
      # mid-way would silently turn the rest into blind ingest.
      t.boolean :open, null: false, default: true

      t.timestamps
    end
    add_index :batches, :open

    # A claim is releasable. A mis-declared batch has to be recoverable without
    # unpicking the library by hand.
    create_table :episode_claims do |t|
      t.references :batch, null: false, foreign_key: true
      t.references :episode, null: false, foreign_key: true
      # unique on the reference itself: t.references already builds an index,
      # and adding a second one under the same name collides. One title can be
      # claimed by exactly one episode.
      t.references :disc_title, null: false, foreign_key: true, index: { unique: true }
      t.timestamps
    end
    add_index :episode_claims, %i[batch_id episode_id], unique: true

    # ---- filing ------------------------------------------------------------

    create_table :placements do |t|
      t.references :disc_title, null: false, foreign_key: true, index: { unique: true }
      t.string :path, null: false
      t.text :nfo
      t.datetime :linked_at
      t.timestamps
    end
    add_index :placements, :path, unique: true

    # ---- provider cache ----------------------------------------------------

    # Not an optimisation. It means identification can be rewritten and re-run
    # against the whole collection offline, with no network and no rate limit --
    # the same reason the rips tree keeps verbatim enumerator output, one layer
    # up. Phase F depends on this existing from the start.
    create_table :provider_cache do |t|
      t.string :provider, null: false
      t.string :endpoint, null: false
      t.string :cache_key, null: false
      t.jsonb :body, null: false, default: {}
      t.datetime :fetched_at, null: false
      t.timestamps
    end
    add_index :provider_cache, %i[provider endpoint cache_key], unique: true,
              name: "index_provider_cache_on_lookup"
  end
end
