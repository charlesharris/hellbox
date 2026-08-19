# Turns a hellboxd event into catalog rows.
#
# The daemon owns facts about bytes and this owns judgments about meaning, so
# ingest is deliberately narrow: it records what the daemon observed and never
# decides anything. Identification runs afterwards, against what landed here.
#
# Everything keys on fingerprint and is idempotent. The stream replays after a
# reconnect, and a disc is published twice by design — once when enumerated and
# again when ripped — so applying the same event twice has to be harmless.
class Ingest
  def self.call(event) = new(event).call

  def initialize(event)
    @kind = event["kind"]
    @data = event["data"] || {}
  end

  def call
    case @kind
    when "disc" then disc!
    end
  end

  private

  def disc!
    fp = @data["fingerprint"].presence or return nil

    disc = Disc.find_or_initialize_by(fingerprint: fp)
    disc.volume_label  = @data["volume_label"]
    disc.disc_type     = @data["disc_type"]
    disc.read_path     = @data["read_path"]
    disc.blocked       = !!@data["blocked"]
    disc.blocked_reason = @data["blocked_reason"].presence

    # An empty name must never overwrite one already recorded. The ripped event
    # carries the same name as the enumerated one, but a future event might not,
    # and losing "The Karate Kid (Special Edition)" to a blank field would be
    # silent and irreversible.
    disc.disc_name = @data["disc_name"] if @data["disc_name"].present?
    disc.rip_dir   = @data["rip_dir"] if @data["rip_dir"].present?

    titles = Array(@data["titles"])
    disc.total_seconds = titles.sum { |t| t["duration_seconds"].to_i }

    # Status only ever moves forward. A replayed "enumerated" event arriving
    # after a disc was confirmed must not drag it back to rough and put it in
    # front of somebody again.
    disc.status = "rough" if disc.new_record?
    disc.ripped_at ||= Time.current if @data["stage"] == "ripped"

    disc.save!
    sync_titles(disc, titles)
    disc
  end

  def sync_titles(disc, titles)
    titles.each do |t|
      row = disc.disc_titles.find_or_initialize_by(index: t["index"].to_i)
      row.duration_seconds = t["duration_seconds"].to_i
      row.chapters         = t["chapters"].to_i
      row.audio_count      = t["audio_count"].to_i
      row.subtitle_count   = t["subtitle_count"].to_i
      row.source_file      = t["source_file"].presence
      if disc.rip_dir.present?
        row.rip_path = File.join(disc.rip_dir, format("title_%02d.mkv", row.index))
      end
      row.save!
    end

    # Titles that vanished between two events would mean the disc was
    # re-enumerated differently — a damaged disc read twice, most likely. Left
    # alone rather than deleted: a stale row is visible and recoverable, and a
    # deleted one takes its placement and episode claim with it.
  end
end
