select order_id, region, amount
from {{ ref('orders') }}
