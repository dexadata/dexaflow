select region, count(*) as orders, sum(amount) as revenue
from {{ ref('stg_orders') }}
group by region
