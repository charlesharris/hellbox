module Library
  # Where the media trees are, as this process sees them.
  #
  # The paths come from the environment rather than a constant because Rails
  # runs in a container and hellboxd does not. Both must name the same files by
  # the same strings — a placement recorded as /srv/media/library/... has to
  # mean the same thing to Jellyfin, which is a third process again — so the
  # compose file mounts /srv/media at the identical path inside and out and
  # these simply read what it set.
  module Paths
    def self.encoded = ENV.fetch("TRANSCODED_DIR", "/srv/media/transcoded")
    def self.library = ENV.fetch("LIBRARY_DIR", "/srv/media/library")

    # Jellyfin's two library roots. It is content-type per root, so films and
    # television cannot share one.
    def self.movies = File.join(library, "Movies")
    def self.tv = File.join(library, "TV")

    # True when path is inside the library tree. Every deletion checks this: a
    # placement row is not on its own sufficient authority to unlink a file,
    # because a wrong path in the database would then be a wrong path deleted
    # off the disk.
    def self.inside_library?(path)
      return false if path.blank?
      File.absolute_path(path).start_with?(File.absolute_path(library) + File::SEPARATOR)
    end
  end
end
