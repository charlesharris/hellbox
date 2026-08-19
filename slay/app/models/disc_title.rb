# One selectable title on a disc: an episode, a feature, or an extra.
class DiscTitle < ApplicationRecord
  belongs_to :disc
  has_one :placement, dependent: :destroy
  has_one :episode_claim, dependent: :destroy

  validates :index, presence: true, uniqueness: { scope: :disc_id }

  def runtime
    total = duration_seconds.to_i
    format("%d:%02d:%02d", total / 3600, (total % 3600) / 60, total % 60)
  end

  # The runtime a metadata provider would list for this title.
  #
  # A PAL disc runs film at 25fps instead of 24, so everything on it measures
  # about 4% shorter than any provider says -- a 24-minute episode arrives as
  # 23. Comparing the raw figure mis-aligns a region 2 disc by one episode,
  # which is the worst way to be wrong because it looks plausible.
  def provider_runtime_seconds
    return duration_seconds unless disc.pal?
    (duration_seconds * 25.0 / 24.0).round
  end
end
