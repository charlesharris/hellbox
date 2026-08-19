require "hellboxd/client"

# The dashboard answers one question: does anything need me?
#
# There are no notifications anywhere in this system by choice, which makes
# this page the only signal there is. Everything else -- library, failures,
# health, history -- is a click away and must never compete with the answer to
# that question.
class DashboardController < ApplicationController
  def show
    @daemon_up = true
    @drives = client.drives
    @health = client.health
  rescue Hellboxd::Unreachable, Hellboxd::Error => e
    # A dead daemon and an idle one look identical in an empty list and mean
    # entirely different things, so the difference is stated rather than shown.
    @daemon_up = false
    @daemon_error = e.message
    @drives = []
    @health = []
  ensure
    @needs_you = needs_you
    @working = Array(@drives).select { |d| WORKING_STATES.include?(d["state"]) }
    @done_today = Disc.recently_filed.where(updated_at: Time.current.all_day).limit(10)
  end

  private

  WORKING_STATES = %w[scanning decrypting ripping verifying encoding ejecting loading].freeze

  # Everything that wants a person, in one list, most actionable first.
  #
  # A disc that cannot be ripped belongs here as much as one that needs naming:
  # both are stuck, and the difference between "tell me what this is" and
  # "this needs MakeMKV" is a sentence, not a screen.
  def needs_you
    items = []

    Disc.needing_review.order(updated_at: :desc).limit(20).each do |disc|
      items << { title: disc.display_name, detail: "which episodes?", disc: disc }
    end

    Disc.where(blocked: true).order(updated_at: :desc).limit(20).each do |disc|
      items << { title: disc.display_name, detail: disc.blocked_reason, disc: disc }
    end

    Array(@drives).select { |d| d["state"] == "failed" }.each do |d|
      items << { title: d["device_path"].to_s, detail: d["error"].to_s, disc: nil }
    end

    items
  end

  def client
    @client ||= Hellboxd::Client.new
  end
end
