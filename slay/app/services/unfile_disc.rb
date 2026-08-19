# Takes a disc back out of the library.
#
# Undo has to be cheap or the auto-file thresholds can never be tuned: the
# first ones will be wrong, and finding that out must cost a click rather than
# an evening. Nothing is re-encoded and nothing is re-read — the bytes live in
# the encoded tree and the library is only ever links onto them, so unfiling is
# removing links and the sidecars that went with them.
#
# It deletes files, so it is deliberate about what it will touch: inside the
# library tree, recorded in `placements`, and a regular file. A wrong path in
# the database is otherwise a wrong file off the disk.
class UnfileDisc
  Result = Struct.new(:disc, :removed, :missing, :refused, keyword_init: true)

  def self.call(disc) = new(disc).call

  def initialize(disc)
    @disc = disc
    @result = Result.new(disc:, removed: [], missing: [], refused: [])
  end

  def call
    dirs = []

    @disc.disc_titles.includes(:placement).each do |title|
      placement = title.placement or next
      path = placement.path

      unless Library::Paths.inside_library?(path)
        # Recorded as somewhere outside the library. Whatever it is, it is not
        # ours to delete; the row goes but the file stays.
        @result.refused << [path, "outside #{Library::Paths.library}"]
        placement.destroy!
        next
      end

      dirs << File.dirname(path)
      remove(path)
      remove(Library::Naming.sidecar(path))
      placement.destroy!
    end

    prune(dirs.uniq)
    @disc.update!(status: "confirmed") if @disc.status == "filed"
    @result
  end

  private

  def remove(path)
    unless File.file?(path)
      @result.missing << path
      return
    end
    File.unlink(path)
    @result.removed << path
  rescue SystemCallError => e
    @result.refused << [path, e.message]
  end

  # An empty "Season 03" left behind shows up in Jellyfin as a season with no
  # episodes, so the directories go too — but only while they are empty, and
  # never above Movies/ or TV/ themselves.
  def prune(dirs)
    roots = [Library::Paths.movies, Library::Paths.tv].map { |r| File.absolute_path(r) }

    dirs.each do |dir|
      current = File.absolute_path(dir)
      while Library::Paths.inside_library?(current) && !roots.include?(current)
        # A series folder holding nothing but the tvshow.nfo this wrote is
        # empty in every sense that matters, and leaving it puts a series with
        # no episodes on the front page.
        entries = children(current)
        if entries == ["tvshow.nfo"]
          remove(File.join(current, "tvshow.nfo"))
          entries = children(current)
        end
        break unless entries.empty?

        begin
          Dir.rmdir(current)
        rescue SystemCallError
          break
        end
        current = File.dirname(current)
      end
    end
  end

  def children(dir)
    Dir.children(dir)
  rescue SystemCallError
    ["."] # unreadable: treat as non-empty, so nothing here is removed
  end
end
