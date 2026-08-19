# Where one title ended up in the library, and what was written beside it.
#
# The unique index on path is the never-clobber rule expressed in the database
# rather than only in the code that writes files: two titles cannot claim the
# same library path even if two filing runs race, and the second one fails
# loudly instead of overwriting the first.
#
# The NFO is stored as well as written. A sidecar can be edited or deleted by
# anything with access to the share, and keeping the text here means the
# library can be rebuilt from the catalog without re-deriving it.
class Placement < ApplicationRecord
  belongs_to :disc_title

  validates :path, presence: true, uniqueness: true

  delegate :disc, to: :disc_title

  def filename = File.basename(path)
end
