# Every provider response, kept.
#
# Not an optimisation. It is what lets identification be rewritten and replayed
# over the whole collection with no network and no rate limit — the same reason
# the rips tree keeps verbatim enumerator output, one layer up. Phase F depends
# on this having existed from the start.
class ProviderCache < ApplicationRecord
  self.table_name = "provider_cache"

  # Responses do not expire. A film's release year does not change, and a stale
  # answer that can be replayed offline is worth more than a fresh one that
  # needs the network to exist.
  def self.fetch(provider:, endpoint:, key:)
    row = find_by(provider: provider, endpoint: endpoint, cache_key: key)
    return row.body if row

    body = yield
    return nil if body.nil?

    create!(provider: provider, endpoint: endpoint, cache_key: key,
            body: body, fetched_at: Time.current)
    body
  rescue ActiveRecord::RecordNotUnique
    find_by(provider: provider, endpoint: endpoint, cache_key: key)&.body
  end
end
