# The library screen: what has been filed, and what is waiting to be.
#
# Its real job is the undo button. The auto-file thresholds in the design are
# guesses that will be wrong at first, and they can only be tuned by letting
# them run and cheaply reversing what they got wrong -- so every filed disc
# stays listed with one click to take it back out.
#
# Filing runs in the request rather than through a job queue. It is a handful
# of hardlinks and a few small files; there is no encoder to wait on, because
# the encoding already happened. Solid Queue is in the Gemfile but nothing runs
# a worker, so a job here would be a job that never ran.
class FilingsController < ApplicationController
  def index
    @filed = Disc.where(status: "filed").order(updated_at: :desc).limit(50)
    @ready = Disc.where(status: "confirmed").order(updated_at: :desc)
    @placements = Placement.where(disc_title: DiscTitle.where(disc: @filed))
                           .includes(disc_title: :disc)
                           .group_by { |p| p.disc_title.disc_id }
  end

  def create
    disc = Disc.find(params[:id])
    result = FileDisc.call(disc)
    redirect_to filings_path, notice: "#{disc.display_name}: #{result.summary}."
  rescue FileDisc::CrossDevice => e
    redirect_to filings_path, alert: e.message
  end

  def destroy
    disc = Disc.find(params[:id])
    result = UnfileDisc.call(disc)
    notice = "Took #{disc.display_name} back out of the library " \
             "(#{result.removed.size} file#{'s' unless result.removed.size == 1} removed)."
    notice += " #{result.refused.size} could not be removed." if result.refused.any?
    redirect_to filings_path, notice:
  end
end
