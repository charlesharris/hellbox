Rails.application.routes.draw do
  get "up" => "rails/health#show", as: :rails_health_check

  resources :reviews, only: %i[index show update] do
    member { post :reopen }
  end

  # Filing is a verb on a disc rather than a resource of its own: there is one
  # library, and a disc is either in it or not.
  get    "library"     => "filings#index",   as: :filings
  post   "library/:id" => "filings#create",  as: :file_disc
  delete "library/:id" => "filings#destroy", as: :unfile_disc

  root "dashboard#show"
end
