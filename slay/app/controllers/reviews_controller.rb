# The review queue: the one screen where a person does work.
#
# Everything else in slay is glanceable. This is where a disc the nets could not
# settle gets settled, and its whole job is to make that take seconds rather
# than minutes — the evidence laid out, the reasoning visible, and the common
# answer one click away.
class ReviewsController < ApplicationController
  def index
    @discs = Disc.where(status: %w[rough needs_review])
                 .includes(:candidates, :disc_titles)
                 .order(updated_at: :desc)
    @blocked = Disc.where(blocked: true).order(updated_at: :desc)
  end

  def show
    @disc = Disc.includes(:candidates, :disc_titles).find(params[:id])
    @best = @disc.candidates.order(confidence: :desc).first
    # Other discs that look like the same set, so "apply to the rest" has
    # something to apply to. Matched on the volume label because that is what a
    # box set shares — six Still Game discs all say STILL_GAME.
    @siblings = siblings_of(@disc)
  end

  def update
    @disc = Disc.find(params[:id])
    Review::Apply.call(@disc, review_params, titles: params[:titles])

    if params[:apply_to_siblings] == "1"
      applied = siblings_of(@disc).each { |s| Review::Apply.call(s, review_params, titles: nil) }
      notice = "Confirmed #{@disc.display_name} and applied the series to #{applied.size} other disc(s)."
    else
      notice = "Confirmed #{@disc.display_name}."
    end

    # Filed here rather than on a later sweep, because a decision that does not
    # visibly do anything is a decision people stop trusting. Siblings are not
    # filed: they were given a title and a kind, not episode numbers, and
    # filing them would be filing a guess.
    notice += " #{file(@disc)}"

    redirect_to reviews_path, notice: notice
  rescue ActiveRecord::RecordInvalid => e
    redirect_to review_path(@disc), alert: e.message
  end

  # Sends a disc back to the queue. A confirmation made in error has to be
  # undoable, or people stop making them.
  def reopen
    disc = Disc.find(params[:id])
    disc.update!(status: "needs_review", confirmed_at: nil)
    redirect_to review_path(disc), notice: "Reopened for review."
  end

  private

  # Filing must never cost a confirmation. The decision is already saved by the
  # time this runs, so a library that cannot be written -- a bad mount, a full
  # disk -- is reported and left for the library screen to retry.
  def file(disc)
    FileDisc.call(disc).summary
  rescue FileDisc::CrossDevice, SystemCallError => e
    "Could not file it: #{e.message}"
  end

  def review_params
    params.permit(:confirmed_title, :confirmed_year, :confirmed_kind,
                  :confirmed_season, :provider, :provider_id)
  end

  def siblings_of(disc)
    return Disc.none if disc.volume_label.blank?
    Disc.where(volume_label: disc.volume_label)
        .where.not(id: disc.id)
        .order(:created_at)
  end
end
