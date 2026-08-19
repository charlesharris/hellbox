# Puts a confirmed disc into the library, where Jellyfin reads it.
#
# Three rules govern everything here.
#
# **Hardlinks, not copies.** The library and the encoded tree are the same
# bytes: filing costs no space and no time, and a later rename keeps pointing
# at the same data. It also means the encoded tree remains the thing that is
# actually backed up, and the library is a view of it.
#
# **Never replace an existing file.** A hardlink onto an occupied path is a
# conflict to surface, not a write to force. Two discs claiming one path is
# either a duplicate or a mis-identification, and both want a person.
#
# **Safe to run again, always.** Titles finish encoding minutes after a disc is
# confirmed, and a disc confirmed while the encoder is still working files in
# stages. So this reports what is not ready rather than failing, and running it
# a second time picks up exactly what was missing. `bin/file-pending` is that
# second run, on a loop.
class FileDisc
  # A hardlink cannot cross a filesystem, and the container makes that easy to
  # get wrong: bind-mounting rips, encoded and library separately puts them on
  # three mounts as the container sees them, even though they are one
  # filesystem on the host. The error is EXDEV and it is worth naming, because
  # the message the kernel gives is "Invalid cross-device link" and the cause
  # is a compose file.
  class CrossDevice < StandardError; end

  Result = Struct.new(:disc, :linked, :pending, :skipped, :conflicts, keyword_init: true) do
    # Filed means every title that was going to be filed is filed. A disc with
    # titles still encoding is not done, and must not be recorded as done, or
    # nothing will ever come back for the rest.
    def complete? = linked.any? && pending.empty? && skipped.empty? && conflicts.empty?

    def summary
      parts = []
      parts << "filed #{linked.size} title#{'s' unless linked.size == 1}" if linked.any?
      parts << "#{pending.size} still encoding" if pending.any?
      parts << "#{skipped.size} skipped" if skipped.any?
      parts << "#{conflicts.size} conflict#{'s' unless conflicts.size == 1}" if conflicts.any?
      parts.empty? ? "nothing to file" : parts.join(", ")
    end
  end

  def self.call(disc, notify: true) = new(disc, notify:).call

  def initialize(disc, notify: true)
    @disc = disc
    @notify = notify
    @result = Result.new(disc:, linked: [], pending: [], skipped: [], conflicts: [])
  end

  def call
    return skip_all("this disc is blocked and was never ripped") if @disc.blocked?
    return skip_all("nothing has confirmed what this disc is") if @disc.confirmed_kind.blank?

    roles = Library::Roles.for(@disc)
    @disc.disc_titles.order(:index).each { |t| place(t, roles[t.index]) }

    write_series_nfo if @disc.confirmed_kind == "series" && @result.linked.any?

    @disc.update!(status: "filed") if @result.complete?
    Jellyfin.notify if @notify && @result.linked.any?

    @result
  end

  private

  # The roles that produce a file. "unknown" means nobody has said and nothing
  # could infer it; "ignore" means a person looked and said no. Both are
  # ordinary answers and neither is an error.
  FILEABLE = %w[feature episode extra].freeze

  def place(title, role)
    return unless FILEABLE.include?(role)

    source = encoded_source(title)
    return @result.pending << title if source.nil?

    dest = destination(title, role)
    return if dest.nil? # destination records its own reason for refusing

    link(title, source, dest, role)
  rescue SystemCallError => e
    @result.skipped << [title, "could not file: #{e.message}"]
  end

  # The encoded file this title became.
  #
  # The daemon mirrors the rips tree under the encoded tree, so the path is
  # derivable rather than needing an event of its own — which matters because
  # it means titles encoded before any of this existed are findable too. What
  # is derived gets written back to the row, so it is looked up once.
  def encoded_source(title)
    return title.encoded_path if title.encoded_path.present? && File.file?(title.encoded_path)
    return nil if @disc.rip_dir.blank?

    guess = File.join(Library::Paths.encoded,
                      File.basename(@disc.rip_dir),
                      format("title_%02d.mkv", title.index))
    return nil unless File.file?(guess)

    title.update!(encoded_path: guess)
    guess
  end

  def destination(title, role)
    film = @disc.confirmed_kind == "movie"
    base = film ? Library::Naming.movie_base(name, @disc.confirmed_year) : Library::Naming.clean(name)
    parent = film ? Library::Naming.movie_dir(name, @disc.confirmed_year) : Library::Naming.series_dir(name)

    case role
    when "feature"
      unless film
        @result.skipped << [title, "marked as the feature on a television disc — make it an episode or an extra"]
        return nil
      end
      Library::Naming.movie_file(name, @disc.confirmed_year)
    when "episode"
      season = title.season_number || @disc.confirmed_season
      if season.nil? || title.episode_number.nil?
        @result.skipped << [title, "no season and episode number — nothing on a disc carries them"]
        return nil
      end
      Library::Naming.episode_file(name, season, title.episode_number, title.episode_title)
    when "extra"
      Library::Naming.extra_file(parent, base, title.episode_title, title.index)
    end
  end

  def link(title, source, dest, role)
    FileUtils.mkdir_p(File.dirname(dest))

    if File.exist?(dest)
      # Already ours, from an earlier run over the same disc. Idempotence is
      # what makes the reconcile sweep safe to run on a timer.
      unless File.identical?(source, dest)
        @result.conflicts << [title, "#{dest} exists already and is a different file"]
        return
      end
    else
      File.link(source, dest)
    end

    nfo = nfo_for(title, role)
    File.write(Library::Naming.sidecar(dest), nfo) if nfo

    record(title, dest, nfo)
    @result.linked << dest
  rescue Errno::EXDEV
    raise CrossDevice, "#{Library::Paths.encoded} and #{Library::Paths.library} are on different " \
                       "mounts, so they cannot be hardlinked. Bind-mount /srv/media as one volume."
  end

  def record(title, dest, nfo)
    placement = Placement.find_or_initialize_by(disc_title_id: title.id)
    placement.path = dest
    placement.nfo = nfo
    placement.linked_at = Time.current
    placement.save!
  end

  def nfo_for(title, role)
    case role
    when "feature"
      Library::Nfo.movie(title: name, year: @disc.confirmed_year,
                         provider: @disc.provider, provider_id: @disc.provider_id)
    when "episode"
      Library::Nfo.episode(show: name,
                           season: title.season_number || @disc.confirmed_season,
                           episode: title.episode_number,
                           title: title.episode_title)
    when "extra"
      Library::Nfo.extra(title: title.episode_title.presence || format("Extra %d", title.index))
    end
  end

  # tvshow.nfo carries the series' provider id, which is what stops Jellyfin
  # re-matching the show by folder name. Written once and never rewritten: a
  # second disc of the same series would write the identical file, and anything
  # a person has edited by hand is theirs.
  def write_series_nfo
    path = File.join(Library::Naming.series_dir(name), "tvshow.nfo")
    return if File.exist?(path)

    FileUtils.mkdir_p(File.dirname(path))
    File.write(path, Library::Nfo.tvshow(title: name, provider: @disc.provider,
                                         provider_id: @disc.provider_id))
  end

  def name = @disc.confirmed_title.presence || @disc.display_name

  def skip_all(reason)
    @disc.disc_titles.each { |t| @result.skipped << [t, reason] }
    @result
  end
end
