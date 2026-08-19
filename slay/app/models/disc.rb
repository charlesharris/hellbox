# A physical disc, as the catalog understands it.
#
# The fingerprint is the join to the daemon's own database and the reason a
# disc reinserted next year is recognised in seconds rather than re-ripped.
# Everything else here is a judgment and may be corrected; nothing here is
# authoritative over the rips tree.
class Disc < ApplicationRecord
  has_many :disc_titles, -> { order(:index) }, dependent: :destroy
  has_many :candidates, -> { order(confidence: :desc) }, dependent: :destroy

  # rough        just arrived, nothing decided
  # needs_review the nets disagreed, or nobody was confident enough
  # confirmed    identified, ready to file
  # filed        hardlinked into the library
  STATUSES = %w[rough needs_review confirmed filed].freeze

  validates :fingerprint, presence: true, uniqueness: true
  validates :status, inclusion: { in: STATUSES }

  scope :needing_review, -> { where(status: "needs_review") }
  scope :recently_filed, -> { where(status: "filed").order(updated_at: :desc) }

  # The name to show a person, in descending order of how much it can be
  # trusted. A Blu-ray's own bdmt_eng.xml is prose someone typed; a volume
  # label is eleven upper-case characters chosen by an authoring house.
  def display_name
    disc_name.presence || volume_label.presence || "Unlabelled disc #{fingerprint.first(12)}"
  end

  def best_candidate
    candidates.first
  end

  # True when the leading candidates are close enough that picking one is
  # nearly a coin toss. A wrong name filed silently is worse than one flagged.
  def contested?
    top = candidates.limit(2).to_a
    top.size == 2 && (top[0].confidence - top[1].confidence) < 0.2
  end

  # What a person needs to know about a disc that cannot be ripped, phrased as
  # a fact about the disc rather than as an error.
  def blocked_summary
    return nil unless blocked?
    "#{display_name} — #{disc_titles.size} titles, #{blocked_reason}"
  end
end
