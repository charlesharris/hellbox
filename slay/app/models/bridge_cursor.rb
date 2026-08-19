# How far the bridge has read the daemon's event stream.
class BridgeCursor < ApplicationRecord
  DEFAULT = "hellboxd".freeze

  def self.for(name = DEFAULT)
    find_or_create_by!(name: name)
  end

  # Advanced only after the event has been applied, never before. A crash
  # between the two must replay the event rather than skip it — applying twice
  # is harmless by design, losing one is not.
  def advance!(id)
    return if id.to_i <= last_event_id
    update!(last_event_id: id.to_i, seen_at: Time.current)
  end
end
